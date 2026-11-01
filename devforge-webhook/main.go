package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
)

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

func (a *App) alertHandler(w http.ResponseWriter, r *http.Request) {
	var payload AlertPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	result := a.handleGroup(payload)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"result": result})
}

func (a *App) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /alert", requireToken(a.cfg.Token, a.alertHandler))
	mux.HandleFunc("POST /slack/actions", a.slackActions)
	mux.HandleFunc("GET /incidents", a.incidentsPage)
	mux.HandleFunc("GET /incidents/{id}", a.incidentPage)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// indexRunbooks embeds the runbooks in the background; Qdrant or Ollama may still be starting.
func indexRunbooks(rag *RAG) {
	for attempt := 1; attempt <= 10; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err := rag.IndexRunbooks(ctx)
		cancel()
		if err == nil {
			return
		}
		slog.Warn("runbook indexing failed, will retry", "attempt", attempt, "error", err.Error())
		time.Sleep(10 * time.Second)
	}
	slog.Error("giving up on runbook indexing; RCAs will not include runbook context")
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg := loadConfig()

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		slog.Error("cannot create data directory", "error", err.Error())
		os.Exit(1)
	}
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		slog.Error("cannot open incident database", "path", cfg.DBPath, "error", err.Error())
		os.Exit(1)
	}
	defer store.Close()

	app := &App{
		cfg: cfg, store: store, rag: NewRAG(cfg),
		kube: func() (kubernetes.Interface, error) { return newKubeClient(cfg) },
	}
	if app.rag != nil {
		go indexRunbooks(app.rag)
	}

	slog.Info("DevForge AI webhook starting", "port", cfg.Port, "model", cfg.OllamaModel, "auth", cfg.Token != "",
		"prometheus", cfg.PrometheusURL != "", "rag", app.rag != nil, "slack_buttons", cfg.SlackSecret != "")
	if err := http.ListenAndServe(":"+cfg.Port, app.mux()); err != nil {
		slog.Error("server error", "error", err.Error())
		os.Exit(1)
	}
}
