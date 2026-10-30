package cmd

import (
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	good := []string{"billing-service", "a", "svc1", "my-api-2"}
	bad := []string{"", "Billing", "1svc", "-svc", "svc-", "my_service", "../etc", "has space", strings.Repeat("a", 41)}
	for _, n := range good {
		if err := validateName(n); err != nil {
			t.Errorf("%q should be valid: %v", n, err)
		}
	}
	for _, n := range bad {
		if err := validateName(n); err == nil {
			t.Errorf("%q should be invalid", n)
		}
	}
}

func TestCamelCase(t *testing.T) {
	if got := camelCase("billing-service"); got != "BillingService" {
		t.Errorf("got %q", got)
	}
}

func TestBuildScaffoldGo(t *testing.T) {
	s, err := buildScaffold(newScaffoldData("billing-service", "postgres"), true)
	if err != nil {
		t.Fatal(err)
	}

	wantApp := []string{"main.go", "go.mod", "Dockerfile", ".github/workflows/ci.yml"}
	for _, f := range wantApp {
		if _, ok := s.App[f]; !ok {
			t.Errorf("app file %s missing", f)
		}
	}
	wantManifests := []string{"deployment.yaml", "service.yaml", "hpa.yaml", "pdb.yaml", "servicemonitor.yaml", "prometheusrule.yaml", "alerts.rules.yml", "grafana-dashboard.json"}
	for _, f := range wantManifests {
		if _, ok := s.Manifests[f]; !ok {
			t.Errorf("manifest %s missing", f)
		}
	}

	// No template delimiters may leak into the output.
	all := map[string]string{}
	for k, v := range s.App {
		all[k] = v
	}
	for k, v := range s.Manifests {
		all["manifests/"+k] = v
	}
	for name, content := range all {
		if strings.Contains(content, "[[") || strings.Contains(content, "]]") {
			t.Errorf("%s still contains template delimiters", name)
		}
	}

	checks := map[string][]string{
		"main.go":                          {`serviceName = "billing-service"`, "/health", "/metrics", "http_requests_total", "http_request_duration_seconds"},
		"manifests/deployment.yaml":        {"readOnlyRootFilesystem: true", "runAsNonRoot: true", "path: /health", "path: /ready", "prometheus.io/scrape", "limits:"},
		"manifests/prometheusrule.yaml":    {"kind: PrometheusRule", "BillingServiceDown", "BillingServiceHighErrorRate", "{{ $labels.pod }}"},
		"manifests/servicemonitor.yaml":    {"kind: ServiceMonitor", "path: /metrics"},
		"manifests/hpa.yaml":               {"HorizontalPodAutoscaler", "minReplicas: 2"},
		"manifests/pdb.yaml":               {"PodDisruptionBudget"},
		"manifests/grafana-dashboard.json": {`"uid": "billing-service-overview"`, "{{route}}"},
		".github/workflows/ci.yml":         {"${{ secrets.DOCKER_USERNAME }}/billing-service", "devforge-manifests-billing-service"},
	}
	for file, wants := range checks {
		content, ok := all[file]
		if !ok {
			t.Errorf("%s not generated", file)
			continue
		}
		for _, w := range wants {
			if !strings.Contains(content, w) {
				t.Errorf("%s should contain %q", file, w)
			}
		}
	}

	// The CI sed rewrites every "image:" line, so the manifest repo's deployment must have exactly one.
	if n := strings.Count(s.Manifests["deployment.yaml"], "image:"); n != 1 {
		t.Errorf("deployment.yaml has %d image: lines, want 1", n)
	}
	// Generated CI must not contain tabs (YAML forbids them for indentation).
	if strings.Contains(s.App[".github/workflows/ci.yml"], "\t") {
		t.Error("ci.yml contains tab characters")
	}
}

func TestBuildScaffoldNonGoSkipsAppTemplate(t *testing.T) {
	s, err := buildScaffold(newScaffoldData("svc", "redis"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.App["main.go"]; ok {
		t.Error("main.go should not be rendered for non-Go languages")
	}
	if _, ok := s.Manifests["deployment.yaml"]; !ok {
		t.Error("manifests should still be rendered")
	}
}
