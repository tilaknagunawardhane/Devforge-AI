package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

type AlertPayload struct {
	Alerts []struct {
		Labels map[string]string `json:"labels"`
	} `json:"alerts"`
}

// requireToken rejects requests without the shared secret. An empty token disables auth.
func requireToken(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func alertHandler(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload AlertPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for _, alert := range payload.Alerts {
			pod := alert.Labels["pod"]
			if pod == "" {
				continue
			}
			ns := alert.Labels["namespace"]
			if ns == "" {
				ns = "default"
			}
			slog.Info("alert received", "pod", pod, "namespace", ns, "alertname", alert.Labels["alertname"])
			// Analyze in the background so Alertmanager gets its 200 immediately.
			go analyze(context.Background(), cfg, pod, ns)
		}
		w.WriteHeader(http.StatusOK)
	}
}

func newMux(cfg Config) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/alert", requireToken(cfg.Token, alertHandler(cfg)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg := loadConfig()
	slog.Info("DevForge AI webhook starting", "port", cfg.Port, "model", cfg.OllamaModel, "auth", cfg.Token != "", "prometheus", cfg.PrometheusURL != "")
	if err := http.ListenAndServe(":"+cfg.Port, newMux(cfg)); err != nil {
		slog.Error("server error", "error", err.Error())
		os.Exit(1)
	}
}
