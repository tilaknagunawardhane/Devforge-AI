package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type OllamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type OllamaResponse struct {
	Response string `json:"response"`
}

func bullet(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return "- " + strings.Join(items, "\n- ")
}

// buildPrompt renders the evidence into a structured prompt. It only states facts we collected.
func buildPrompt(ev Evidence) string {
	logs := ev.Logs
	if logs == "" {
		logs = "(none available)"
	}
	stats := strings.TrimSpace(ev.PromStats)
	if stats == "" {
		stats = "(none available)"
	}
	return fmt.Sprintf(`You are a Senior Site Reliability Engineer.
Pod "%s" in namespace "%s" triggered an alert. Use ONLY the evidence below; if the evidence does not show a cause, say so and do not invent one.

Kubernetes events for the pod:
%s

Pods in the same ReplicaSet:
%s

Recent logs:
%s

Metrics:
%s

Data that could not be collected:
%s

Write a Root Cause Analysis for Slack (bold with *asterisks*, a few emojis, under 180 words) with exactly these sections:
*Summary* - what happened.
*Likely cause* - what the evidence supports, with a confidence level (low/medium/high).
*Evidence* - the specific lines or facts you relied on.
*Suggested fix* - concrete next steps.`,
		ev.Pod, ev.Namespace, bullet(ev.Events), bullet(ev.Siblings), logs, stats, bullet(ev.Unavailable))
}

// fallbackReport is posted when the LLM cannot be reached, so the team still gets the facts.
func fallbackReport(ev Evidence, reason error) string {
	return fmt.Sprintf(":warning: *Alert for pod `%s`* (namespace `%s`)\nAI analysis unavailable (%v). Raw evidence:\n*Events*\n%s\n*Pods*\n%s",
		ev.Pod, ev.Namespace, reason, bullet(ev.Events), bullet(ev.Siblings))
}

// askOllama retries with exponential backoff; each attempt has its own timeout.
func askOllama(ctx context.Context, cfg Config, prompt string) (string, error) {
	body, _ := json.Marshal(OllamaRequest{Model: cfg.OllamaModel, Prompt: prompt, Stream: false})
	var lastErr error
	for attempt := 1; attempt <= cfg.OllamaRetries; attempt++ {
		text, err := ollamaOnce(ctx, cfg, body)
		if err == nil {
			return text, nil
		}
		lastErr = err
		slog.Warn("ollama attempt failed", "attempt", attempt, "error", err.Error())
		if attempt < cfg.OllamaRetries {
			select {
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	return "", lastErr
}

func ollamaOnce(ctx context.Context, cfg Config, body []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.OllamaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.OllamaURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var or OllamaResponse
	if err := json.Unmarshal(raw, &or); err != nil {
		return "", err
	}
	if strings.TrimSpace(or.Response) == "" {
		return "", fmt.Errorf("ollama returned an empty response")
	}
	return or.Response, nil
}

func sendToSlack(cfg Config, report string) {
	if cfg.SlackWebhookURL == "" {
		slog.Info("SLACK_WEBHOOK_URL not set, printing report instead", "report", report)
		return
	}
	payload, _ := json.Marshal(map[string]string{"text": report})
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(cfg.SlackWebhookURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		slog.Error("slack post failed", "error", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Error("slack returned an error", "status", resp.StatusCode)
		return
	}
	slog.Info("RCA posted to Slack")
}

// analyze collects evidence, asks the LLM, and delivers the result.
func analyze(ctx context.Context, cfg Config, pod, namespace string) {
	ev := Evidence{Pod: pod, Namespace: namespace}

	if cs, err := newKubeClient(cfg); err != nil {
		ev.Unavailable = append(ev.Unavailable, "Kubernetes API: "+err.Error())
	} else {
		collectKubeEvidence(ctx, cs, &ev, cfg.LogTailLines)
	}
	collectPromEvidence(ctx, cfg, &ev)

	slog.Info("evidence collected", "pod", pod, "events", len(ev.Events), "siblings", len(ev.Siblings), "has_logs", ev.Logs != "")

	report, err := askOllama(ctx, cfg, buildPrompt(ev))
	if err != nil {
		slog.Error("RCA generation failed, sending fallback", "pod", pod, "error", err.Error())
		report = fallbackReport(ev, err)
	}
	sendToSlack(cfg, report)
}
