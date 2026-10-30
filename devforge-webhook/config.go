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
	Token           string // shared secret expected as "Authorization: Bearer <token>"; empty disables auth
	PrometheusURL   string
	KubeAPIHost     string // optional override of the API server address in a kubeconfig
	LogTailLines    int64
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
		Token:           token,
		PrometheusURL:   os.Getenv("PROMETHEUS_URL"),
		KubeAPIHost:     os.Getenv("KUBE_API_HOST"),
		LogTailLines:    int64(getenvInt("LOG_TAIL_LINES", 30)),
	}
}
