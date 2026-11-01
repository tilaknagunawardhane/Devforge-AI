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

	// Auto-remediation. Nothing runs without a human approval; these are the limits around it.
	RemediationNamespaces []string      // actions may only touch these namespaces; empty disables execution
	RemediationDryRun     bool          // approved actions are recorded but change nothing
	RemediationCooldown   time.Duration // minimum gap between executed actions on the same workload
	ProposalTTL           time.Duration // a proposal can no longer be approved after this long
	Approvers             []string      // Slack / API user names allowed to approve; empty = anyone who can reach the endpoint
	RecentRolloutWindow   time.Duration // "crash loop after a rollout" only counts rollouts newer than this

	GitHubToken        string // fine-grained token with contents + pull-requests write on the manifest repos
	GitHubAPI          string
	GitHubRepoTemplate string // e.g. "your-user/devforge-manifests-{deployment}"
	GitHubManifestPath string
	MaxMemoryMi        int // the PR rule never proposes a memory limit above this
}

func getenvList(key string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(key), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
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

		RemediationNamespaces: getenvList("REMEDIATION_NAMESPACES"),
		RemediationDryRun:     strings.EqualFold(os.Getenv("REMEDIATION_DRY_RUN"), "true"),
		RemediationCooldown:   time.Duration(getenvInt("REMEDIATION_COOLDOWN_SECONDS", 300)) * time.Second,
		ProposalTTL:           time.Duration(getenvInt("PROPOSAL_TTL_SECONDS", 1800)) * time.Second,
		Approvers:             getenvList("REMEDIATION_APPROVERS"),
		RecentRolloutWindow:   time.Duration(getenvInt("RECENT_ROLLOUT_SECONDS", 1800)) * time.Second,

		GitHubToken:        os.Getenv("GITHUB_TOKEN"),
		GitHubAPI:          strings.TrimRight(getenv("GITHUB_API_URL", "https://api.github.com"), "/"),
		GitHubRepoTemplate: os.Getenv("GITHUB_REPO_TEMPLATE"),
		GitHubManifestPath: getenv("GITHUB_MANIFEST_PATH", "base/deployment.yaml"),
		MaxMemoryMi:        getenvInt("REMEDIATION_MAX_MEMORY_MI", 1024),
	}
}
