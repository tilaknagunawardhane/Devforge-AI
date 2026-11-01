package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const sampleRunbook = `# Pod restarts

Intro text before any heading.

## Symptoms
Pods restart repeatedly or show CrashLoopBackOff.

## Fix
Check logs with kubectl logs --previous, then raise memory limits.
`

func TestChunkMarkdown(t *testing.T) {
	chunks := chunkMarkdown("pod-restarts.md", sampleRunbook)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3: %+v", len(chunks), chunks)
	}
	if chunks[0].Source != "runbook Pod restarts" || !strings.Contains(chunks[0].Text, "Intro text") {
		t.Errorf("intro chunk: %+v", chunks[0])
	}
	if chunks[1].Source != "runbook Pod restarts - Symptoms" || chunks[2].Source != "runbook Pod restarts - Fix" {
		t.Errorf("sources: %q, %q", chunks[1].Source, chunks[2].Source)
	}
	if strings.Contains(chunks[1].Text, "##") || strings.Contains(chunks[1].Text, "raise memory") {
		t.Errorf("chunk text must not leak neighbours: %q", chunks[1].Text)
	}
	if chunks[1].ID == chunks[2].ID {
		t.Error("chunk IDs must be unique")
	}
	first, again := pointID(chunks[1].ID), pointID(chunks[1].ID)
	if first != again || pointID("a") == pointID("b") {
		t.Error("pointID must be stable and distinct")
	}
}

// fakeStack stands in for both Ollama (embeddings) and Qdrant.
type fakeStack struct {
	mu          sync.Mutex
	collections map[string]int
	points      []map[string]any
}

func newFakeStack(t *testing.T) (*httptest.Server, *fakeStack) {
	f := &fakeStack{collections: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.URL.Path == "/api/embeddings":
			json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{1, 0, 0}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/collections/"):
			if _, ok := f.collections[r.URL.Path]; !ok {
				http.Error(w, "not found", 404)
				return
			}
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/points"):
			f.points = append(f.points, body["points"].([]any)[0].(map[string]any))
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPut:
			f.collections[r.URL.Path] = int(body["vectors"].(map[string]any)["size"].(float64))
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/points/search"):
			var result []map[string]any
			for _, p := range f.points {
				result = append(result, map[string]any{"score": 0.9, "payload": p["payload"]})
			}
			json.NewEncoder(w).Encode(map[string]any{"result": result})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 500)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func TestRAGIndexAndSearch(t *testing.T) {
	srv, fake := newFakeStack(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pod-restarts.md"), []byte(sampleRunbook), 0o644)

	rag := NewRAG(Config{QdrantURL: srv.URL, OllamaURL: srv.URL, EmbedModel: "e", RunbookDir: dir,
		QdrantCollec: "c", RAGResults: 3, RAGMinScore: 0.4})

	n, err := rag.IndexRunbooks(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("IndexRunbooks = (%d, %v), want 3 chunks", n, err)
	}
	if fake.collections["/collections/c"] != 3 {
		t.Errorf("collection should be sized to the embedding (3), got %v", fake.collections)
	}

	hits, err := rag.Search(context.Background(), "pod keeps restarting")
	if err != nil || len(hits) != 3 || hits[0].Kind != "runbook" {
		t.Fatalf("Search = (%+v, %v)", hits, err)
	}

	// Past incidents are stored with the same mechanism and are retrievable.
	if err := rag.RememberIncident(context.Background(), Incident{ID: 7, Subject: "web", Namespace: "default", Alertnames: "A"}, "memory limit too low"); err != nil {
		t.Fatal(err)
	}
	hits, _ = rag.Search(context.Background(), "x")
	found := false
	for _, h := range hits {
		if h.Kind == "incident" && strings.Contains(h.Source, "#7") && strings.Contains(h.Text, "memory limit too low") {
			found = true
		}
	}
	if !found {
		t.Errorf("past incident not retrievable: %+v", hits)
	}
}

func TestRAGDisabledAndUnreachable(t *testing.T) {
	if NewRAG(Config{}) != nil {
		t.Error("without QDRANT_URL the RAG must be disabled (nil)")
	}
	rag := NewRAG(Config{QdrantURL: "http://127.0.0.1:1", OllamaURL: "http://127.0.0.1:1", EmbedModel: "e"})
	if _, err := rag.Search(context.Background(), "q"); err == nil {
		t.Error("expected an error when the services are down")
	}
}
