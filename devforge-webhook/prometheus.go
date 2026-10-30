package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type promResponse struct {
	Data struct {
		Result []struct {
			Value [2]interface{} `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// promQuery runs an instant query and returns the first sample value as a string.
func promQuery(ctx context.Context, base, query string) (string, error) {
	u := fmt.Sprintf("%s/api/v1/query?query=%s", base, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return "", err
	}
	if len(pr.Data.Result) == 0 {
		return "0", nil
	}
	return fmt.Sprint(pr.Data.Result[0].Value[1]), nil
}

func collectPromEvidence(ctx context.Context, cfg Config, ev *Evidence) {
	if cfg.PrometheusURL == "" {
		ev.Unavailable = append(ev.Unavailable, "Prometheus metrics (PROMETHEUS_URL not set)")
		return
	}
	queries := []struct{ label, q string }{
		{"pods hit by chaos faults in this namespace, last 10m", fmt.Sprintf(`count(chaos_last_injection_timestamp_seconds{namespace=%q} > (time() - 600)) or vector(0)`, ev.Namespace)},
		{"pods hit by chaos faults in any namespace, last 1h", `count(chaos_last_injection_timestamp_seconds > (time() - 3600)) or vector(0)`},
	}
	for _, q := range queries {
		v, err := promQuery(ctx, cfg.PrometheusURL, q.q)
		if err != nil {
			ev.Unavailable = append(ev.Unavailable, "Prometheus: "+err.Error())
			return
		}
		ev.PromStats += fmt.Sprintf("%s: %s\n", q.label, v)
	}
}
