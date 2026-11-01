package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings, read from environment variables.
type Config struct {
	Port            string
	OllamaURL       string
	OllamaModel     string
	OllamaTimeout   time.Duration
	OllamaRetries   int
	SlackWebhookURL string
	SlackSecret     string // Slack signing secret; when set, messages get buttons and /slack/actions is enabled
	Token           string // shared secret expected as "Authorization: Bearer <token>"; empty disables auth
	PrometheusURL   string
	KubeAPIHost     string // optional override of the API server address in a kubeconfig
	LogTailLines    int64
	MaxEvidencePods int // how many pods of one incident get their own events collected

	DBPath       string        // SQLite file holding incidents
	DedupWindow  time.Duration // alerts for the same group within this window join the open incident
	PublicURL    string        // base URL used in links back to /incidents
	QdrantURL    string        // empty disables runbook / past-incident retrieval
	EmbedModel   string        // Ollama model used for embeddings
	RunbookDir   string
	RAGMinScore  float64
	RAGResults   int
	QdrantCollec string
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return fallback
}

func getenvFloat(key string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return v
	}
	return fallback
}

func loadConfig() Config {
	token := os.Getenv("WEBHOOK_TOKEN")
	if f := os.Getenv("WEBHOOK_TOKEN_FILE"); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	return Config{
		Port:            getenv("PORT", "9090"),
		OllamaURL:       getenv("OLLAMA_URL", "http://host.docker.internal:11434"),
		OllamaModel:     getenv("OLLAMA_MODEL", "llama3"),
		OllamaTimeout:   time.Duration(getenvInt("OLLAMA_TIMEOUT_SECONDS", 180)) * time.Second,
		OllamaRetries:   getenvInt("OLLAMA_RETRIES", 3),
		SlackWebhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
		SlackSecret:     os.Getenv("SLACK_SIGNING_SECRET"),
		Token:           token,
		PrometheusURL:   os.Getenv("PROMETHEUS_URL"),
		KubeAPIHost:     os.Getenv("KUBE_API_HOST"),
		LogTailLines:    int64(getenvInt("LOG_TAIL_LINES", 30)),
		MaxEvidencePods: getenvInt("MAX_EVIDENCE_PODS", 3),

		DBPath:       getenv("DB_PATH", "/data/incidents.db"),
		DedupWindow:  time.Duration(getenvInt("DEDUP_WINDOW_SECONDS", 600)) * time.Second,
		PublicURL:    strings.TrimRight(getenv("PUBLIC_URL", "http://localhost:9090"), "/"),
		QdrantURL:    strings.TrimRight(os.Getenv("QDRANT_URL"), "/"),
		EmbedModel:   getenv("EMBED_MODEL", "nomic-embed-text"),
		RunbookDir:   getenv("RUNBOOK_DIR", "/runbooks"),
		RAGMinScore:  getenvFloat("RAG_MIN_SCORE", 0.4),
		RAGResults:   getenvInt("RAG_RESULTS", 3),
		QdrantCollec: getenv("QDRANT_COLLECTION", "devforge"),
	}
}
