package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBackends(t *testing.T) (webhook, prom *httptest.Server) {
	webhook = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/incidents.json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`[{"id":3,"subject":"<script>x</script>","namespace":"shop","status":"open","alertCount":2,
			"pods":["a","b"],"pendingActions":1,"createdAt":"2026-01-01T10:00:00Z","url":"http://wh/incidents/3"}]`))
	}))
	prom = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		val := "none"
		switch {
		case strings.Contains(q, `"healthy"`) && strings.HasPrefix(q, "sum("):
			val = "2"
		case strings.Contains(q, `"healthy"`) && strings.HasPrefix(q, "count("):
			val = "2"
		case strings.Contains(q, `"degraded"`) && strings.HasPrefix(q, "sum("):
			val = "1"
		case strings.Contains(q, `"degraded"`) && strings.HasPrefix(q, "count("):
			val = "2"
		}
		if val == "none" {
			w.Write([]byte(`{"data":{"result":[]}}`))
			return
		}
		w.Write([]byte(`{"data":{"result":[{"value":[0,"` + val + `"]}]}}`))
	}))
	t.Cleanup(webhook.Close)
	t.Cleanup(prom.Close)
	return webhook, prom
}

func seedCatalog(t *testing.T, names ...string) {
	t.Setenv("DEVFORGE_CATALOG", filepath.Join(t.TempDir(), "catalog.json"))
	for _, n := range names {
		if err := recordService(CatalogEntry{Name: n, Language: "go", Database: "redis", Path: "./" + n, CreatedAt: time.Now().UTC(),
			AppRepo: "https://github.com/u/devforge-app-" + n}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPortalShowsServicesStatusAndIncidents(t *testing.T) {
	seedCatalog(t, "healthy", "degraded", "silent")
	webhook, prom := fakeBackends(t)
	cfg := PortalConfig{WebhookURL: webhook.URL, WebhookPublic: "http://public-webhook", PrometheusURL: prom.URL, GrafanaURL: "http://grafana", ArgoCDURL: "http://argo"}

	srv := httptest.NewServer(portalHandler(cfg))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	page := string(raw)

	for _, want := range []string{"healthy", "2/2 pods up", "1/2 pods up", "no metrics yet", "http://grafana/d/healthy-overview",
		"http://argo/applications/argocd/healthy-dev", "1 action", "http://wh/incidents/3", "shop", "http://public-webhook/audit"} {
		if !strings.Contains(page, want) {
			t.Errorf("portal should contain %q", want)
		}
	}
	if strings.Contains(page, "<script>x</script>") {
		t.Error("incident text from the webhook must be HTML-escaped")
	}
}

func TestPortalSurvivesMissingBackends(t *testing.T) {
	seedCatalog(t, "svc")
	cfg := PortalConfig{WebhookURL: "http://127.0.0.1:1", PrometheusURL: "http://127.0.0.1:1"}
	d := buildPortalData(context.Background(), cfg)

	if len(d.Services) != 1 || d.Services[0].Status != "status unavailable" {
		t.Errorf("service should degrade, got %+v", d.Services)
	}
	if len(d.Notes) != 1 || !strings.Contains(d.Notes[0], "Incidents unavailable") {
		t.Errorf("expected one note about incidents, got %v", d.Notes)
	}
	// With nothing configured at all the page still renders.
	srv := httptest.NewServer(portalHandler(PortalConfig{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("portal with no config: %v %v", resp, err)
	}
	resp.Body.Close()
}

func TestPortalEmptyCatalogAndAPI(t *testing.T) {
	seedCatalog(t)
	srv := httptest.NewServer(portalHandler(PortalConfig{}))
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/api/services")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Errorf("empty catalog should be [], got %q", raw)
	}
	resp, _ = http.Get(srv.URL + "/healthz")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("healthz = %d", resp.StatusCode)
	}
	resp, _ = http.Get(srv.URL + "/nope")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("unknown path = %d, want 404", resp.StatusCode)
	}
}
