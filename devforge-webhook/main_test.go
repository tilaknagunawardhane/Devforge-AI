package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

func TestAlertHandlerRejectsBadJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	alertHandler(Config{})(rec, httptest.NewRequest(http.MethodPost, "/alert", strings.NewReader("{not json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

func TestBuildPromptIncludesEvidence(t *testing.T) {
	ev := Evidence{
		Pod:         "demo-app-abc-123",
		Namespace:   "default",
		Events:      []string{"Normal Killing: Stopping container nginx"},
		Unavailable: []string{"pod logs"},
	}
	p := buildPrompt(ev)
	for _, want := range []string{"demo-app-abc-123", "Stopping container nginx", "pod logs", "*Suggested fix*", "(none available)"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
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
	ev := Evidence{Pod: "p1", Namespace: "default", Events: []string{"Warning BackOff: restarting"}}
	r := fallbackReport(ev, context.DeadlineExceeded)
	for _, want := range []string{"p1", "AI analysis unavailable", "BackOff"} {
		if !strings.Contains(r, want) {
			t.Errorf("fallback missing %q", want)
		}
	}
}
