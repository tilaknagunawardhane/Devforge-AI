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
		"components/prometheus-operator/servicemonitor.yaml", "components/prometheus-operator/prometheusrule.yaml", "components/prometheus-operator/kustomization.yaml",
		"base/kustomization.yaml", "argocd/application-dev.yaml", "argocd/application-staging.yaml",
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
		"base/deployment.yaml":                               {"readOnlyRootFilesystem: true", "runAsNonRoot: true", "path: /health", "path: /ready", "prometheus.io/scrape", "limits:"},
		"components/prometheus-operator/prometheusrule.yaml": {"kind: PrometheusRule", "BillingServiceDown", "BillingServiceHighErrorRate", "{{ $labels.pod }}"},
		"components/prometheus-operator/servicemonitor.yaml": {"kind: ServiceMonitor", "path: /metrics"},
		"components/prometheus-operator/kustomization.yaml":  {"kind: Component", "servicemonitor.yaml", "prometheusrule.yaml"},
		"base/kustomization.yaml":                            {"deployment.yaml", "service.yaml"},
		"overlays/dev/kustomization.yaml":                    {"namespace: billing-service-dev", "../../base", "$patch: delete", "environment: dev"},
		"overlays/staging/kustomization.yaml":                {"namespace: billing-service-staging", "../../base", "environment: staging"},
		"overlays/dev/namespace.yaml":                        {"name: billing-service-dev"},
		"monitoring/grafana-dashboard.json":                  {`"uid": "billing-service-overview"`, "{{route}}"},
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

func TestWithSLO(t *testing.T) {
	d, err := newScaffoldData("svc", "go", "redis").WithSLO(99.9)
	if err != nil {
		t.Fatal(err)
	}
	if d.SLO != "99.9" || d.Budget != "0.001" || d.FastBurn != "0.0144" || d.SlowBurn != "0.006" {
		t.Errorf("99.9%% SLO: %+v", d)
	}
	if d.ErrPerMin != 29 || d.OkPerMin != 971 {
		t.Errorf("test traffic = %d/%d, want 29 failing of 1000", d.ErrPerMin, d.OkPerMin)
	}
	loose, _ := newScaffoldData("svc", "go", "redis").WithSLO(95)
	if loose.ErrPerMin != 1000 || loose.OkPerMin != 0 {
		t.Errorf("a 95%% SLO needs every request failing to burn fast, got %d/%d", loose.ErrPerMin, loose.OkPerMin)
	}
	for _, bad := range []float64{89.9, 100, 0, -1} {
		if _, err := newScaffoldData("svc", "go", "redis").WithSLO(bad); err == nil {
			t.Errorf("SLO %v should be rejected", bad)
		}
	}
}

func TestSLORulesGenerated(t *testing.T) {
	data, _ := newScaffoldData("billing-service", "go", "postgres").WithSLO(99.5)
	s, err := buildScaffold(data)
	if err != nil {
		t.Fatal(err)
	}
	rules := s.Manifests["monitoring/alerts.rules.yml"]
	for _, want := range []string{"job:http_error_ratio:rate5m", "job:http_error_ratio:rate6h", "BillingServiceErrorBudgetFastBurn",
		"BillingServiceErrorBudgetSlowBurn", "> 0.072", "> 0.03", "99.5% error budget"} {
		if !strings.Contains(rules, want) {
			t.Errorf("alerts.rules.yml should contain %q", want)
		}
	}
	// The same rules are wrapped for the Prometheus Operator, and a promtool test ships alongside.
	if !strings.Contains(s.Manifests["components/prometheus-operator/prometheusrule.yaml"], "ErrorBudgetFastBurn") {
		t.Error("PrometheusRule should include the SLO alerts")
	}
	if test := s.Manifests["monitoring/alerts.rules.test.yml"]; !strings.Contains(test, "alertname: BillingServiceErrorBudgetFastBurn") {
		t.Error("promtool test missing")
	}
}

// base/ must apply on any cluster, so it may not contain kinds that need extra CRDs.
func TestBaseNeedsNoCRDs(t *testing.T) {
	for _, lang := range SupportedLanguages {
		s, err := buildScaffold(newScaffoldData("svc", lang, "redis"))
		if err != nil {
			t.Fatal(err)
		}
		base := s.Manifests["base/kustomization.yaml"]
		for _, crd := range []string{"servicemonitor", "prometheusrule"} {
			if strings.Contains(base, crd) {
				t.Errorf("base/kustomization.yaml must not include %s", crd)
			}
		}
		for _, env := range []string{"dev", "staging"} {
			if o := s.Manifests["overlays/"+env+"/kustomization.yaml"]; strings.Contains(o, "prometheus-operator") {
				t.Errorf("%s overlay includes the operator component without --prometheus-operator", env)
			}
		}
	}
}

func TestPrometheusOperatorOptIn(t *testing.T) {
	data := newScaffoldData("svc", "go", "redis").WithPlatform("team-a", "alice", "", true)
	s, err := buildScaffold(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "staging"} {
		o := s.Manifests["overlays/"+env+"/kustomization.yaml"]
		if !strings.Contains(o, "components:\n  - ../../components/prometheus-operator") {
			t.Errorf("%s overlay should include the component:\n%s", env, o)
		}
		if !strings.Contains(o, "namespace.yaml\ncomponents") {
			t.Errorf("%s overlay layout unexpected:\n%s", env, o)
		}
	}
}

func TestArgoCDApplications(t *testing.T) {
	data := newScaffoldData("billing-service", "go", "postgres").WithPlatform("team-a", "alice", "", false)
	s, err := buildScaffold(data)
	if err != nil {
		t.Fatal(err)
	}
	dev := s.Manifests["argocd/application-dev.yaml"]
	for _, want := range []string{"kind: Application", "name: billing-service-dev", "namespace: argocd",
		"repoURL: https://github.com/alice/devforge-manifests-billing-service.git", "path: overlays/dev",
		"namespace: billing-service-dev", "prune: true", "selfHeal: true", "CreateNamespace=true"} {
		if !strings.Contains(dev, want) {
			t.Errorf("application-dev.yaml should contain %q", want)
		}
	}
	if st := s.Manifests["argocd/application-staging.yaml"]; !strings.Contains(st, "path: overlays/staging") || !strings.Contains(st, "billing-service-staging") {
		t.Errorf("staging application wrong:\n%s", st)
	}

	custom := newScaffoldData("svc", "go", "redis").WithPlatform("t", "alice", "http://gitea:3000/devforge/svc.git", false)
	c, _ := buildScaffold(custom)
	if !strings.Contains(c.Manifests["argocd/application-dev.yaml"], "repoURL: http://gitea:3000/devforge/svc.git") {
		t.Error("--manifest-repo-url must override the default")
	}
}

func TestBackstageCatalogInfo(t *testing.T) {
	data := newScaffoldData("billing-service", "python", "postgres").WithPlatform("payments-team", "alice", "", false)
	s, _ := buildScaffold(data)
	info := s.App["catalog-info.yaml"]
	for _, want := range []string{"apiVersion: backstage.io/v1alpha1", "kind: Component", "name: billing-service",
		"github.com/project-slug: alice/devforge-app-billing-service", "argocd/app-name: billing-service-dev",
		"type: service", "owner: payments-team", "- python"} {
		if !strings.Contains(info, want) {
			t.Errorf("catalog-info.yaml should contain %q:\n%s", want, info)
		}
	}
}
