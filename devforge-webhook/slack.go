package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// slackPayload builds the message. With a signing secret configured (so /slack/actions can verify
// button clicks) it adds buttons; otherwise it is plain text. Proposed remediation actions are always
// listed, with Approve / Reject buttons when interactivity is on.
func slackPayload(cfg Config, i Incident, report string, proposals []Action) []byte {
	header := fmt.Sprintf(":rotating_light: *Incident #%d* - %s (%d alert%s)\n%s/incidents/%d",
		i.ID, i.Subject, i.AlertCount, plural(i.AlertCount), cfg.PublicURL, i.ID)
	text := header + "\n" + report
	if len(proposals) > 0 {
		text += "\n\n:robot_face: *Proposed actions* (nothing runs until a person approves):"
		for _, p := range proposals {
			text += fmt.Sprintf("\n- #%d `%s %s/%s`: %s", p.ID, p.Kind, p.Namespace, p.Target, p.Reason)
			if cfg.SlackSecret == "" {
				text += fmt.Sprintf("\n  approve with `POST %s/actions/%d/approve`", cfg.PublicURL, p.ID)
			}
		}
	}
	msg := map[string]any{"text": text}
	if cfg.SlackSecret != "" {
		id := strconv.FormatInt(i.ID, 10)
		button := func(action, value, label, style string) map[string]any {
			return map[string]any{"type": "button", "action_id": action, "value": value, "style": style,
				"text": map[string]string{"type": "plain_text", "text": label}}
		}
		withConfirm := func(b map[string]any, title, body, ok string) map[string]any {
			b["confirm"] = map[string]any{
				"title":   map[string]string{"type": "plain_text", "text": title},
				"text":    map[string]string{"type": "mrkdwn", "text": body},
				"confirm": map[string]string{"type": "plain_text", "text": ok},
				"deny":    map[string]string{"type": "plain_text", "text": "Cancel"},
			}
			return b
		}
		blocks := []any{
			map[string]any{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": trim(text, 2900)}},
		}
		var propButtons []any
		for _, p := range proposals {
			v := strconv.FormatInt(p.ID, 10)
			propButtons = append(propButtons,
				withConfirm(button("approve", v, fmt.Sprintf("Approve #%d %s", p.ID, p.Kind), "primary"),
					"Approve this action?", fmt.Sprintf("`%s %s/%s`\n%s", p.Kind, p.Namespace, p.Target, p.Reason), "Approve"),
				button("reject", v, fmt.Sprintf("Reject #%d", p.ID), "danger"))
		}
		if len(propButtons) > 0 {
			blocks = append(blocks, map[string]any{"type": "actions", "elements": propButtons})
		}
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []any{
			button("ack", id, "Acknowledge", "primary"),
			withConfirm(button("restart", id, "Restart workload", "danger"), "Restart the workload?",
				"This triggers a rolling restart of the Deployment behind the alerting pod(s).", "Restart"),
			withConfirm(button("rollback", id, "Roll back", "danger"), "Roll back the workload?",
				"This puts the previous ReplicaSet's pod template back into the Deployment.", "Roll back"),
		}})
		msg["blocks"] = blocks
	}
	b, _ := json.Marshal(msg)
	return b
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (a *App) postSlack(i Incident, report string, proposals []Action) {
	if a.cfg.SlackWebhookURL == "" {
		slog.Info("SLACK_WEBHOOK_URL not set, printing report instead", "incident", i.ID, "report", report)
		return
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(a.cfg.SlackWebhookURL, "application/json",
		bytes.NewReader(slackPayload(a.cfg, i, report, proposals)))
	if err != nil {
		slog.Error("slack post failed", "error", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Error("slack returned an error", "status", resp.StatusCode)
		return
	}
	slog.Info("RCA posted to Slack", "incident", i.ID)
}

// verifySlack checks Slack's request signature (HMAC-SHA256 of "v0:timestamp:body") and rejects
// requests older than five minutes to stop replays.
func verifySlack(secret string, header http.Header, body []byte, now time.Time) error {
	ts := header.Get("X-Slack-Request-Timestamp")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("missing or invalid timestamp")
	}
	if d := now.Sub(time.Unix(sec, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return fmt.Errorf("timestamp outside the 5 minute window")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(header.Get("X-Slack-Signature"))) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// signSlack is the inverse of verifySlack; used by tests to build valid requests.
func signSlack(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

type slackInteraction struct {
	User struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}

func slackReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{"response_type": "in_channel", "replace_original": false, "text": text})
}

// slackActions handles button clicks. It is authenticated by Slack's signature, not the bearer token.
func (a *App) slackActions(w http.ResponseWriter, r *http.Request) {
	if a.cfg.SlackSecret == "" {
		http.Error(w, "slack interactivity is not configured", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := verifySlack(a.cfg.SlackSecret, r.Header, body, time.Now()); err != nil {
		slog.Warn("slack signature rejected", "error", err.Error())
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var in slackInteraction
	if err := json.Unmarshal([]byte(form.Get("payload")), &in); err != nil || len(in.Actions) == 0 {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	user := in.User.Username
	if user == "" {
		user = in.User.Name
	}
	act := in.Actions[0]
	id, err := strconv.ParseInt(act.Value, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}

	switch act.ActionID {
	case "approve", "reject":
		// value is the ACTION id here; the decision goes through the same policy and audit path as the API
		msg, err := a.decide(r.Context(), id, user, act.ActionID == "approve")
		if err != nil {
			slackReply(w, ":no_entry: "+err.Error())
			return
		}
		slackReply(w, ":gear: "+msg)
		return
	case "ack", "restart", "rollback":
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	inc, err := a.store.Get(id)
	if err != nil {
		slackReply(w, fmt.Sprintf("Incident #%d was not found.", id))
		return
	}
	switch act.ActionID {
	case "ack":
		if err := a.store.SetStatus(id, StatusAcknowledged, user); err != nil {
			slackReply(w, "Could not acknowledge: "+err.Error())
			return
		}
		a.audit(user, "acknowledged", id, 0, "incident acknowledged from Slack")
		// A human has now vouched for this incident, so its analysis may inform future ones.
		// Skip fallback reports: they hold raw evidence, not an analysis.
		if a.rag != nil && inc.RCA != "" && !strings.HasPrefix(inc.RCA, ":warning:") {
			go func(i Incident) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				if err := a.rag.RememberIncident(ctx, i, i.RCA); err != nil {
					slog.Warn("could not index acknowledged incident", "incident", i.ID, "error", err.Error())
				}
			}(*inc)
		}
		slackReply(w, fmt.Sprintf(":white_check_mark: Incident #%d acknowledged by %s.", id, user))
	default: // restart, rollback
		msg, err := a.manualAction(r.Context(), inc, act.ActionID, user)
		if err != nil {
			slackReply(w, fmt.Sprintf(":x: %s for incident #%d did not run: %v", act.ActionID, id, err))
			return
		}
		slackReply(w, ":arrows_counterclockwise: "+msg)
	}
}

// deploymentName guesses the Deployment from a pod name: pod "web-5877c4b47d-vvp5m" belongs to
// ReplicaSet "web-5877c4b47d" which belongs to Deployment "web".
func deploymentName(pod string) string {
	return siblingPrefix(siblingPrefix(pod))
}
