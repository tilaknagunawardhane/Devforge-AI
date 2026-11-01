package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
)

// newTestApp builds an App with a temp SQLite database and no Kubernetes / Qdrant.
func newTestApp(t *testing.T, cfg Config) *App {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if cfg.DedupWindow == 0 {
		cfg.DedupWindow = time.Hour
	}
	cfg.OllamaRetries, cfg.OllamaTimeout = 1, time.Second
	cfg.OllamaURL = "http://127.0.0.1:1" // unreachable: analysis falls back, which is fine for these tests
	return &App{cfg: cfg, store: store, kube: func() (kubernetes.Interface, error) { return nil, io.EOF }}
}

func TestSiblingPrefix(t *testing.T) {
	cases := map[string]string{
		"demo-app-5877c4b47d-vvp5m": "demo-app-5877c4b47d",
		"standalone":                "standalone",
	}
	for in, want := range cases {
		if got := siblingPrefix(in); got != want {
			t.Errorf("siblingPrefix(%q) = %q, want %q", in, got, want)
		}
	}
	if got := deploymentName("demo-app-5877c4b47d-vvp5m"); got != "demo-app" {
		t.Errorf("deploymentName = %q", got)
	}
}

func TestRequireToken(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	cases := []struct {
		name, token, header string
		want                int
	}{
		{"auth disabled", "", "", 200},
		{"missing header", "secret", "", 401},
		{"wrong token", "secret", "Bearer nope", 401},
		{"correct token", "secret", "Bearer secret", 200},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/alert", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		rec := httptest.NewRecorder()
		requireToken(c.token, ok)(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestAlertEndpointAuthAndBadJSON(t *testing.T) {
	app := newTestApp(t, Config{Token: "secret"})
	srv := httptest.NewServer(app.mux())
	defer srv.Close()

	post := func(body, auth string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/alert", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(`{}`, ""); got != 401 {
		t.Errorf("no token: got %d", got)
	}
	if got := post(`{not json`, "Bearer secret"); got != 400 {
		t.Errorf("bad json: got %d", got)
	}
	if got := post(`{"alerts":[]}`, "Bearer secret"); got != 200 {
		t.Errorf("empty group: got %d", got)
	}
}

func TestBuildPromptIncludesEvidence(t *testing.T) {
	ev := Evidence{
		Subject: "demo-app", Namespace: "default",
		Pods:        []string{"demo-app-abc-123"},
		Alerts:      []string{"ChaosFaultInjected"},
		Events:      []string{"Normal Killing: Stopping container nginx"},
		Timeline:    []TimelineEntry{{time.Date(2026, 1, 1, 10, 0, 5, 0, time.UTC), "pod killed"}, {time.Date(2026, 1, 1, 10, 0, 1, 0, time.UTC), "alert fired"}},
		Context:     []Hit{{Source: "runbook pod-restarts - Symptoms", Text: "check restarts"}},
		Unavailable: []string{"pod logs"},
	}
	p := buildPrompt(ev)
	for _, want := range []string{"demo-app-abc-123", "ChaosFaultInjected", "Stopping container nginx", "pod logs",
		"*Timeline*", "*Suggested fix*", "[runbook pod-restarts - Symptoms] check restarts", "(none available)"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Index(p, "10:00:01 alert fired") > strings.Index(p, "10:00:05 pod killed") {
		t.Error("timeline must be sorted oldest first")
	}
}

func TestAskOllamaRetriesThenSucceeds(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			http.Error(w, "loading model", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"response":"all good"}`))
	}))
	defer srv.Close()

	cfg := Config{OllamaURL: srv.URL, OllamaModel: "m", OllamaTimeout: 5 * time.Second, OllamaRetries: 3}
	got, err := askOllama(context.Background(), cfg, "hi")
	if err != nil || got != "all good" {
		t.Fatalf("got (%q, %v), want (all good, nil)", got, err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestAskOllamaFailsAfterRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := Config{OllamaURL: srv.URL, OllamaModel: "m", OllamaTimeout: 5 * time.Second, OllamaRetries: 1}
	if _, err := askOllama(context.Background(), cfg, "hi"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestFallbackReport(t *testing.T) {
	ev := Evidence{Subject: "p1", Namespace: "default", Events: []string{"Warning BackOff: restarting"}}
	r := fallbackReport(ev, context.DeadlineExceeded)
	for _, want := range []string{"p1", "AI analysis unavailable", "BackOff"} {
		if !strings.Contains(r, want) {
			t.Errorf("fallback missing %q", want)
		}
	}
}

func TestIncidentPagesEscapeContent(t *testing.T) {
	app := newTestApp(t, Config{})
	inc, _ := app.store.Create(Incident{Key: "k", Subject: "<script>alert(1)</script>", Namespace: "default"})
	app.store.SetRCA(inc.ID, "<img src=x onerror=alert(1)>")
	srv := httptest.NewServer(app.mux())
	defer srv.Close()

	for _, path := range []string{"/incidents", "/incidents/1"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || strings.Contains(string(body), "<script>alert") || strings.Contains(string(body), "<img src=x") {
			t.Errorf("%s: status %d, unescaped content in page", path, resp.StatusCode)
		}
	}
	if resp, _ := http.Get(srv.URL + "/incidents/999"); resp.StatusCode != 404 {
		t.Errorf("missing incident: got %d, want 404", resp.StatusCode)
	}
	if resp, _ := http.Get(srv.URL + "/incidents/abc"); resp.StatusCode != 404 {
		t.Errorf("bad id: got %d, want 404", resp.StatusCode)
	}
}

func decodeResult(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	return out["result"]
}
