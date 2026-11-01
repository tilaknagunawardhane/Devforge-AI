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

func contextBlock(hits []Hit) string {
	if len(hits) == 0 {
		return "(none found)"
	}
	var parts []string
	for _, h := range hits {
		parts = append(parts, fmt.Sprintf("[%s] %s", h.Source, trim(h.Text, 700)))
	}
	return strings.Join(parts, "\n\n")
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
An incident in namespace "%s" involves: %s (alerts: %s). Use ONLY the evidence below; if the evidence does not show a cause, say so and do not invent one.

Timeline (UTC, oldest first):
%s

Kubernetes events:
%s

Pods in the same ReplicaSets:
%s

Recent logs:
%s

Metrics:
%s

Relevant runbook sections and similar past incidents (cite the bracketed source name when you use one):
%s

Data that could not be collected:
%s

Write a Root Cause Analysis for Slack (bold with *asterisks*, a few emojis, under 220 words) with exactly these sections:
*Summary* - what happened.
*Timeline* - 3 to 5 bullets with times, taken only from the timeline above.
*Likely cause* - what the evidence supports, with a confidence level (low/medium/high).
*Evidence* - the specific lines or facts you relied on.
*Suggested fix* - concrete next steps, following a cited runbook where one applies.`,
		ev.Namespace, strings.Join(ev.Pods, ", "), strings.Join(ev.Alerts, ", "),
		bullet(renderTimeline(ev.Timeline)), bullet(ev.Events), bullet(ev.Siblings), logs, stats,
		contextBlock(ev.Context), bullet(ev.Unavailable))
}

// fallbackReport is posted when the LLM cannot be reached, so the team still gets the facts.
func fallbackReport(ev Evidence, reason error) string {
	return fmt.Sprintf(":warning: *Incident: %s* (namespace `%s`)\nAI analysis unavailable (%v). Raw evidence:\n*Timeline*\n%s\n*Events*\n%s\n*Pods*\n%s",
		ev.Subject, ev.Namespace, reason, bullet(renderTimeline(ev.Timeline)), bullet(ev.Events), bullet(ev.Siblings))
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
