package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCatalogRoundTrip(t *testing.T) {
	t.Setenv("DEVFORGE_CATALOG", filepath.Join(t.TempDir(), "sub", "catalog.json"))

	if entries, err := loadCatalog(); err != nil || len(entries) != 0 {
		t.Fatalf("empty catalog: got (%v, %v)", entries, err)
	}

	now := time.Now().UTC()
	if err := recordService(CatalogEntry{Name: "b-svc", Language: "go", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := recordService(CatalogEntry{Name: "a-svc", Language: "python", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Re-creating a service replaces its entry instead of duplicating it.
	if err := recordService(CatalogEntry{Name: "b-svc", Language: "node", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	entries, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "a-svc" || entries[1].Language != "node" {
		t.Errorf("unexpected catalog: %+v", entries)
	}
}

func TestAskOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":"# hello"}`))
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_URL", srv.URL)

	got, err := askOllama(context.Background(), "hi")
	if err != nil || got != "# hello" {
		t.Errorf("got (%q, %v)", got, err)
	}
}

func TestAskOllamaServerDown(t *testing.T) {
	t.Setenv("OLLAMA_URL", "http://127.0.0.1:1")
	if _, err := askOllama(context.Background(), "hi"); err == nil {
		t.Error("expected an error when Ollama is unreachable")
	}
}

func TestReadmePromptOnlyUsesScaffoldFacts(t *testing.T) {
	p := readmePrompt(newScaffoldData("svc", "python", "redis"), []string{"main.py", "Dockerfile"})
	for _, want := range []string{`"svc"`, "python", "redis", "/metrics", "main.py, Dockerfile", "do not invent"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt should contain %q", want)
		}
	}
}
