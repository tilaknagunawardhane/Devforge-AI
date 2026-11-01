package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RAG gives the model relevant runbook sections and similar past incidents.
// Vectors live in Qdrant (free, runs in Docker); embeddings come from the local Ollama.
// Everything here is best-effort: if Qdrant or the embedding model is down, the RCA still works.
type RAG struct {
	cfg    Config
	http   *http.Client
	dim    int // vector size, learned from the first embedding
	hasCol bool
}

type Chunk struct {
	ID     string // stable, so re-indexing replaces instead of duplicating
	Source string // e.g. "runbooks/pod-restarts.md - Symptoms"
	Kind   string // "runbook" or "incident"
	Text   string
}

type Hit struct {
	Score  float64
	Source string
	Kind   string
	Text   string
}

func NewRAG(cfg Config) *RAG {
	if cfg.QdrantURL == "" {
		return nil
	}
	return &RAG{cfg: cfg, http: &http.Client{Timeout: 60 * time.Second}}
}

// chunkMarkdown splits a runbook at "## " headings so each chunk is one self-contained topic.
// Text before the first heading is kept under the document title.
func chunkMarkdown(file, content string) []Chunk {
	var chunks []Chunk
	title, heading := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)), ""
	var buf []string
	flush := func() {
		text := strings.TrimSpace(strings.Join(buf, "\n"))
		if text == "" {
			return
		}
		source := "runbook " + title
		if heading != "" {
			source += " - " + heading
		}
		chunks = append(chunks, Chunk{ID: "runbook:" + file + ":" + heading, Source: source, Kind: "runbook", Text: text})
		buf = nil
	}
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "# ") && heading == "" && len(buf) == 0:
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
		case strings.HasPrefix(line, "## "):
			flush()
			heading = strings.TrimSpace(strings.TrimPrefix(line, "## "))
		default:
			buf = append(buf, line)
		}
	}
	flush()
	return chunks
}

// pointID turns a chunk ID into the unsigned integer Qdrant wants.
func pointID(id string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(id))
	return h.Sum64()
}

func (r *RAG) post(ctx context.Context, method, url string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s returned %d: %s", method, url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (r *RAG) embed(ctx context.Context, text string) ([]float64, error) {
	var out struct {
		Embedding []float64 `json:"embedding"`
	}
	err := r.post(ctx, http.MethodPost, r.cfg.OllamaURL+"/api/embeddings",
		map[string]string{"model": r.cfg.EmbedModel, "prompt": text}, &out)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("embedding model %q returned no vector", r.cfg.EmbedModel)
	}
	return out.Embedding, nil
}

// ensureCollection creates the Qdrant collection on first use, sized to the embedding model.
func (r *RAG) ensureCollection(ctx context.Context, dim int) error {
	if r.hasCol && r.dim == dim {
		return nil
	}
	base := r.cfg.QdrantURL + "/collections/" + r.cfg.QdrantCollec
	if err := r.post(ctx, http.MethodGet, base, nil, nil); err != nil {
		err = r.post(ctx, http.MethodPut, base, map[string]any{
			"vectors": map[string]any{"size": dim, "distance": "Cosine"},
		}, nil)
		if err != nil {
			return err
		}
	}
	r.hasCol, r.dim = true, dim
	return nil
}

func (r *RAG) Upsert(ctx context.Context, c Chunk) error {
	vec, err := r.embed(ctx, c.Text)
	if err != nil {
		return err
	}
	if err := r.ensureCollection(ctx, len(vec)); err != nil {
		return err
	}
	return r.post(ctx, http.MethodPut, r.cfg.QdrantURL+"/collections/"+r.cfg.QdrantCollec+"/points?wait=true", map[string]any{
		"points": []map[string]any{{
			"id":      pointID(c.ID),
			"vector":  vec,
			"payload": map[string]string{"source": c.Source, "kind": c.Kind, "text": c.Text},
		}},
	}, nil)
}

// Search returns up to RAGResults chunks scoring at least RAGMinScore.
func (r *RAG) Search(ctx context.Context, query string) ([]Hit, error) {
	vec, err := r.embed(ctx, query)
	if err != nil {
		return nil, err
	}
	if err := r.ensureCollection(ctx, len(vec)); err != nil {
		return nil, err
	}
	var out struct {
		Result []struct {
			Score   float64           `json:"score"`
			Payload map[string]string `json:"payload"`
		} `json:"result"`
	}
	err = r.post(ctx, http.MethodPost, r.cfg.QdrantURL+"/collections/"+r.cfg.QdrantCollec+"/points/search", map[string]any{
		"vector": vec, "limit": r.cfg.RAGResults, "with_payload": true, "score_threshold": r.cfg.RAGMinScore,
	}, &out)
	if err != nil {
		return nil, err
	}
	var hits []Hit
	for _, h := range out.Result {
		hits = append(hits, Hit{Score: h.Score, Source: h.Payload["source"], Kind: h.Payload["kind"], Text: h.Payload["text"]})
	}
	return hits, nil
}

// IndexRunbooks embeds every Markdown file in RunbookDir. Safe to run on every start.
func (r *RAG) IndexRunbooks(ctx context.Context) (int, error) {
	files, _ := filepath.Glob(filepath.Join(r.cfg.RunbookDir, "*.md"))
	n := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return n, err
		}
		for _, c := range chunkMarkdown(filepath.Base(f), string(raw)) {
			if err := r.Upsert(ctx, c); err != nil {
				return n, err
			}
			n++
		}
	}
	slog.Info("runbooks indexed", "chunks", n, "files", len(files))
	return n, nil
}

// RememberIncident stores a finished RCA so similar future incidents can retrieve it.
func (r *RAG) RememberIncident(ctx context.Context, i Incident, rca string) error {
	return r.Upsert(ctx, Chunk{
		ID:     fmt.Sprintf("incident:%d", i.ID),
		Source: fmt.Sprintf("past incident #%d (%s)", i.ID, i.Subject),
		Kind:   "incident",
		Text:   fmt.Sprintf("Incident %s in %s, alerts: %s.\n%s", i.Subject, i.Namespace, i.Alertnames, rca),
	})
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
