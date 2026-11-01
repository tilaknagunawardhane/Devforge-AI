package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	mux.HandleFunc("POST /actions/{id}/approve", requireToken(a.cfg.Token, a.actionDecision(true)))
	mux.HandleFunc("POST /actions/{id}/reject", requireToken(a.cfg.Token, a.actionDecision(false)))
	mux.HandleFunc("GET /audit", a.auditPage)
	mux.HandleFunc("GET /incidents", a.incidentsPage)
	mux.HandleFunc("GET /incidents/{id}", a.incidentPage)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// actionDecision approves or rejects a proposed action over HTTP (bearer token). The acting user is
// taken from the X-Actor header and defaults to "api"; with REMEDIATION_APPROVERS set it must be listed.
func (a *App) actionDecision(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "bad action id", http.StatusBadRequest)
			return
		}
		actor := r.Header.Get("X-Actor")
		if actor == "" {
			actor = "api"
		}
		msg, err := a.decide(r.Context(), id, actor, approve)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			code := http.StatusInternalServerError
			switch {
			case errors.Is(err, ErrNotFound):
				code = http.StatusNotFound
			case errors.Is(err, ErrConflict):
				code = http.StatusConflict
			case errors.Is(err, ErrBlocked):
				code = http.StatusForbidden
			}
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"message": msg})
	}
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
		kube:      func() (kubernetes.Interface, error) { return newKubeClient(cfg) },
		verifyFor: 2 * time.Minute, verifyEvery: 3 * time.Second,
	}
	if app.rag != nil {
		go indexRunbooks(app.rag)
	}

	slog.Info("DevForge AI webhook starting", "port", cfg.Port, "model", cfg.OllamaModel, "auth", cfg.Token != "",
		"prometheus", cfg.PrometheusURL != "", "rag", app.rag != nil, "slack_buttons", cfg.SlackSecret != "",
		"remediation_namespaces", cfg.RemediationNamespaces, "remediation_dry_run", cfg.RemediationDryRun, "github_prs", cfg.GitHubToken != "")
	if err := http.ListenAndServe(":"+cfg.Port, app.mux()); err != nil {
		slog.Error("server error", "error", err.Error())
		os.Exit(1)
	}
}
