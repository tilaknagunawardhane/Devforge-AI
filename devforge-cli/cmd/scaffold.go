package cmd

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

//go:embed templates
var templateFS embed.FS

// ScaffoldData is the input to every template. Templates use [[ ]] delimiters so that
// Prometheus ({{ $labels.pod }}), Grafana ({{route}}) and GitHub Actions (${{ ... }})
// syntax can appear in them untouched.
type ScaffoldData struct {
	Name        string
	AlertPrefix string // CamelCase version of Name, used in alert names
	Database    string
	Port        int
}

// Scaffold is the rendered output: app files go in the app repo, manifests in the manifest repo.
type Scaffold struct {
	App       map[string]string // output path -> content
	Manifests map[string]string // output path (relative to the manifests dir) -> content
}

// Template source -> output path.
var (
	goAppTemplates = map[string]string{
		"templates/go/main.go.tmpl":    "main.go",
		"templates/go/go.mod.tmpl":     "go.mod",
		"templates/go/Dockerfile.tmpl": "Dockerfile",
	}
	ciTemplates = map[string]string{
		"templates/ci/ci.yml.tmpl": ".github/workflows/ci.yml",
	}
	manifestTemplates = map[string]string{
		"templates/manifests/deployment.yaml.tmpl":        "deployment.yaml",
		"templates/manifests/service.yaml.tmpl":           "service.yaml",
		"templates/manifests/hpa.yaml.tmpl":               "hpa.yaml",
		"templates/manifests/pdb.yaml.tmpl":               "pdb.yaml",
		"templates/manifests/servicemonitor.yaml.tmpl":    "servicemonitor.yaml",
		"templates/manifests/alerts.rules.yml.tmpl":       "alerts.rules.yml",
		"templates/manifests/grafana-dashboard.json.tmpl": "grafana-dashboard.json",
	}
)

// A valid Kubernetes DNS label; also keeps the name safe for file paths and repo names.
var validName = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,38}[a-z0-9])?$`)

func validateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid service name %q: use lowercase letters, digits and '-', start with a letter, max 40 characters", name)
	}
	return nil
}

func camelCase(name string) string {
	var b strings.Builder
	for _, part := range strings.Split(name, "-") {
		if part != "" {
			b.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}
	return b.String()
}

func newScaffoldData(name, database string) ScaffoldData {
	return ScaffoldData{Name: name, AlertPrefix: camelCase(name), Database: database, Port: 8080}
}

func renderTemplate(src string, data ScaffoldData) (string, error) {
	raw, err := templateFS.ReadFile(src)
	if err != nil {
		return "", err
	}
	t, err := template.New(src).Delims("[[", "]]").Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	var out bytes.Buffer
	if err := t.Execute(&out, data); err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	return out.String(), nil
}

func renderSet(set map[string]string, data ScaffoldData) (map[string]string, error) {
	out := map[string]string{}
	for src, dst := range set {
		content, err := renderTemplate(src, data)
		if err != nil {
			return nil, err
		}
		out[dst] = content
	}
	return out, nil
}

// prometheusRuleManifest wraps the plain Prometheus rule groups in a PrometheusRule resource
// (Prometheus Operator), so one rule source serves both setups.
func prometheusRuleManifest(name, groups string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: monitoring.coreos.com/v1\nkind: PrometheusRule\nmetadata:\n  name: %s\n  labels:\n    app.kubernetes.io/name: %s\nspec:\n", name, name)
	for _, line := range strings.Split(strings.TrimRight(groups, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// buildScaffold renders everything that does not depend on the language: CI and the
// Kubernetes/observability manifests. If includeGoApp is set, the Go service is rendered too.
func buildScaffold(data ScaffoldData, includeGoApp bool) (*Scaffold, error) {
	s := &Scaffold{App: map[string]string{}}

	ci, err := renderSet(ciTemplates, data)
	if err != nil {
		return nil, err
	}
	for k, v := range ci {
		s.App[k] = v
	}
	if includeGoApp {
		app, err := renderSet(goAppTemplates, data)
		if err != nil {
			return nil, err
		}
		for k, v := range app {
			s.App[k] = v
		}
	}

	s.Manifests, err = renderSet(manifestTemplates, data)
	if err != nil {
		return nil, err
	}
	s.Manifests["prometheusrule.yaml"] = prometheusRuleManifest(data.Name, s.Manifests["alerts.rules.yml"])
	return s, nil
}
