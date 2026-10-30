package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Structs for the Ollama API Request/Response
type OllamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type OllamaResponse struct {
	Response string `json:"response"`
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// askOllama sends a prompt to a local Ollama server. No paid API or cloud service is involved.
// Configure with OLLAMA_URL (default http://host.docker.internal:11434) and OLLAMA_MODEL (default llama3).
func askOllama(ctx context.Context, prompt string) (string, error) {
	url := envOr("OLLAMA_URL", "http://host.docker.internal:11434")
	body, _ := json.Marshal(OllamaRequest{Model: envOr("OLLAMA_MODEL", "llama3"), Prompt: prompt})

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to connect to Ollama at %s: %w", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out OllamaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Response) == "" {
		return "", fmt.Errorf("ollama returned an empty response")
	}
	return out.Response, nil
}

// readmePrompt describes the generated service to the model using only facts from the scaffold,
// so the README cannot contradict what was actually generated.
func readmePrompt(data ScaffoldData, appFiles []string) string {
	return fmt.Sprintf(`Write a concise README.md in Markdown for a microservice named %q.
Facts (use only these; do not invent features):
- Language: %s
- Database: %s (connection string read from the DATABASE_URL environment variable; no driver is wired in yet)
- Endpoints: / (service info), /health (liveness), /ready (readiness), /metrics (Prometheus metrics) on port %d
- Files: %s
- Build and run: docker build -t %s . && docker run -p %d:%d %s
- CI: GitHub Actions runs tests, a gitleaks secret scan and a Trivy image scan before pushing the image.
Sections: Overview, Run locally, Endpoints, Configuration, CI/CD. Keep it under 250 words.`,
		data.Name, data.Language, data.Database, data.Port, strings.Join(appFiles, ", "),
		data.Name, data.Port, data.Port, data.Name)
}

// generateReadme asks the local model for a README. It is optional: callers should warn and carry on if it fails.
func generateReadme(ctx context.Context, data ScaffoldData, appFiles []string) (string, error) {
	return askOllama(ctx, readmePrompt(data, appFiles))
}
