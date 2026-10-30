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

func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{"go": "go", "Golang": "go", "python": "python", "fastapi": "python", "node": "node", "javascript": "node", "JS": "node"}
	for in, want := range cases {
		got, err := normalizeLanguage(in)
		if err != nil || got != want {
			t.Errorf("normalizeLanguage(%q) = (%q, %v), want %q", in, got, err, want)
		}
	}
	if _, err := normalizeLanguage("cobol"); err == nil {
		t.Error("cobol should be rejected")
	}
}

func TestCamelCase(t *testing.T) {
	if got := camelCase("billing-service"); got != "BillingService" {
		t.Errorf("got %q", got)
	}
}

// Files every language must produce, whatever its stack.
func TestBuildScaffoldAllLanguages(t *testing.T) {
	wantApp := map[string][]string{
		"go":     {"main.go", "main_test.go", "go.mod", "Dockerfile"},
		"python": {"main.py", "test_main.py", "requirements.txt", "requirements-dev.txt", "Dockerfile"},
		"node":   {"index.js", "index.test.js", "package.json", "Dockerfile"},
	}
	wantManifests := []string{
		"base/deployment.yaml", "base/service.yaml", "base/hpa.yaml", "base/pdb.yaml",
		"base/servicemonitor.yaml", "base/prometheusrule.yaml", "base/kustomization.yaml",
		"overlays/dev/kustomization.yaml", "overlays/dev/namespace.yaml",
		"overlays/staging/kustomization.yaml", "overlays/staging/namespace.yaml",
		"monitoring/alerts.rules.yml", "monitoring/grafana-dashboard.json",
	}

	for _, lang := range SupportedLanguages {
		t.Run(lang, func(t *testing.T) {
			s, err := buildScaffold(newScaffoldData("billing-service", lang, "postgres"))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range append(wantApp[lang], ".github/workflows/ci.yml") {
				if _, ok := s.App[f]; !ok {
					t.Errorf("app file %s missing", f)
				}
			}
			for _, f := range wantManifests {
				if _, ok := s.Manifests[f]; !ok {
					t.Errorf("manifest %s missing", f)
				}
			}

			// No template delimiters may leak into any output, and the name must be substituted.
			for name, content := range merge(s) {
				if strings.Contains(content, "[[") || strings.Contains(content, "]]") {
					t.Errorf("%s still contains template delimiters", name)
				}
			}

			// Every language exposes the same endpoints and metric names that alerts depend on.
			main := s.App[wantApp[lang][0]]
			for _, want := range []string{"billing-service", "/health", "/ready", "/metrics", "http_requests_total", "http_request_duration_seconds"} {
				if !strings.Contains(main, want) {
					t.Errorf("%s should contain %q", wantApp[lang][0], want)
				}
			}

			// The CI sed rewrites every "image:" line, so the base deployment must have exactly one.
			if n := strings.Count(s.Manifests["base/deployment.yaml"], "image:"); n != 1 {
				t.Errorf("base/deployment.yaml has %d image: lines, want 1", n)
			}
		})
	}
}

func merge(s *Scaffold) map[string]string {
	all := map[string]string{}
	for k, v := range s.App {
		all[k] = v
	}
	for k, v := range s.Manifests {
		all["manifests/"+k] = v
	}
	return all
}

func TestManifestContents(t *testing.T) {
	s, err := buildScaffold(newScaffoldData("billing-service", "go", "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		"base/deployment.yaml":                {"readOnlyRootFilesystem: true", "runAsNonRoot: true", "path: /health", "path: /ready", "prometheus.io/scrape", "limits:"},
		"base/prometheusrule.yaml":            {"kind: PrometheusRule", "BillingServiceDown", "BillingServiceHighErrorRate", "{{ $labels.pod }}"},
		"base/servicemonitor.yaml":            {"kind: ServiceMonitor", "path: /metrics"},
		"base/kustomization.yaml":             {"deployment.yaml", "prometheusrule.yaml"},
		"overlays/dev/kustomization.yaml":     {"namespace: billing-service-dev", "../../base", "$patch: delete", "environment: dev"},
		"overlays/staging/kustomization.yaml": {"namespace: billing-service-staging", "../../base", "environment: staging"},
		"overlays/dev/namespace.yaml":         {"name: billing-service-dev"},
		"monitoring/grafana-dashboard.json":   {`"uid": "billing-service-overview"`, "{{route}}"},
	}
	for file, wants := range checks {
		content, ok := s.Manifests[file]
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
}

func TestCIPipeline(t *testing.T) {
	for _, tc := range []struct{ lang, testCmd, setup string }{
		{"go", "go test ./...", "actions/setup-go"},
		{"python", "pytest", "actions/setup-python"},
		{"node", "npm test", "actions/setup-node"},
	} {
		s, err := buildScaffold(newScaffoldData("svc", tc.lang, "redis"))
		if err != nil {
			t.Fatal(err)
		}
		ci := s.App[".github/workflows/ci.yml"]
		for _, want := range []string{tc.testCmd, tc.setup, "gitleaks", "aquasecurity/trivy-action", "base/deployment.yaml", "devforge-manifests-svc", "${{ secrets.DOCKER_USERNAME }}/svc"} {
			if !strings.Contains(ci, want) {
				t.Errorf("%s ci.yml should contain %q", tc.lang, want)
			}
		}
		// Only the matching language's test setup may be present.
		for _, other := range []string{"actions/setup-go", "actions/setup-python", "actions/setup-node"} {
			if other != tc.setup && strings.Contains(ci, other) {
				t.Errorf("%s ci.yml should not contain %q", tc.lang, other)
			}
		}
		if strings.Contains(ci, "\t") {
			t.Errorf("%s ci.yml contains tab characters", tc.lang)
		}
	}
}

func TestBuildScaffoldUnknownLanguage(t *testing.T) {
	if _, err := buildScaffold(newScaffoldData("svc", "cobol", "redis")); err == nil {
		t.Error("expected an error for an unknown language")
	}
}
