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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// slackPayload builds the message. With a signing secret configured (so /slack/actions can verify
// button clicks) it adds Acknowledge and Restart buttons; otherwise it is plain text.
func slackPayload(cfg Config, i Incident, report string) []byte {
	header := fmt.Sprintf(":rotating_light: *Incident #%d* - %s (%d alert%s)\n%s/incidents/%d",
		i.ID, i.Subject, i.AlertCount, plural(i.AlertCount), cfg.PublicURL, i.ID)
	msg := map[string]any{"text": header + "\n" + report}
	if cfg.SlackSecret != "" {
		id := strconv.FormatInt(i.ID, 10)
		msg["blocks"] = []any{
			map[string]any{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": trim(header+"\n\n"+report, 2900)}},
			map[string]any{"type": "actions", "elements": []any{
				map[string]any{"type": "button", "action_id": "ack", "value": id, "style": "primary",
					"text": map[string]string{"type": "plain_text", "text": "Acknowledge"}},
				map[string]any{"type": "button", "action_id": "restart", "value": id, "style": "danger",
					"text": map[string]string{"type": "plain_text", "text": "Restart workload"},
					"confirm": map[string]any{
						"title":   map[string]string{"type": "plain_text", "text": "Restart the workload?"},
						"text":    map[string]string{"type": "mrkdwn", "text": "This triggers a rolling restart of the Deployment behind the alerting pod(s)."},
						"confirm": map[string]string{"type": "plain_text", "text": "Restart"},
						"deny":    map[string]string{"type": "plain_text", "text": "Cancel"},
					}},
			}},
		}
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

func (a *App) postSlack(i Incident, report string) {
	if a.cfg.SlackWebhookURL == "" {
		slog.Info("SLACK_WEBHOOK_URL not set, printing report instead", "incident", i.ID, "report", report)
		return
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(a.cfg.SlackWebhookURL, "application/json",
		bytes.NewReader(slackPayload(a.cfg, i, report)))
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
	json.NewEncoder(w).Encode(map[string]any{"response_type": "in_channel", "replace_original": false, "text": text})
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
		http.Error(w, "bad incident id", http.StatusBadRequest)
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
		slog.Info("incident acknowledged", "incident", id, "by", user)
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
	case "restart":
		name, err := a.restartDeployment(r.Context(), inc)
		if err != nil {
			slog.Error("restart failed", "incident", id, "error", err.Error())
			slackReply(w, fmt.Sprintf(":x: Restart for incident #%d failed: %v", id, err))
			return
		}
		slog.Info("deployment restarted", "incident", id, "deployment", name, "by", user)
		slackReply(w, fmt.Sprintf(":arrows_counterclockwise: %s restarted deployment `%s` for incident #%d.", user, name, id))
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// deploymentName guesses the Deployment from a pod name: pod "web-5877c4b47d-vvp5m" belongs to
// ReplicaSet "web-5877c4b47d" which belongs to Deployment "web".
func deploymentName(pod string) string {
	return siblingPrefix(siblingPrefix(pod))
}

// restartDeployment performs a rolling restart (what `kubectl rollout restart` does) after checking
// the guessed Deployment really exists.
func (a *App) restartDeployment(ctx context.Context, inc *Incident) (string, error) {
	pods := inc.PodList()
	if len(pods) == 0 {
		return "", fmt.Errorf("this incident names no pods, so there is no workload to restart")
	}
	cs, err := a.kube()
	if err != nil {
		return "", fmt.Errorf("cannot reach Kubernetes: %w", err)
	}
	name := deploymentName(pods[0])
	deployments := cs.AppsV1().Deployments(inc.Namespace)
	if _, err := deployments.Get(ctx, name, metav1.GetOptions{}); err != nil {
		return "", fmt.Errorf("deployment %q not found in %s: %w", name, inc.Namespace, err)
	}
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339))
	if _, err := deployments.Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return "", err
	}
	return name, nil
}
