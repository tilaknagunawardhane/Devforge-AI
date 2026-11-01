package cmd

import (
	"bytes"
	"embed"
	"fmt"
	"math"
	"regexp"
	"strconv"
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
	Language    string // go, python or node
	Database    string
	Port        int
	Env         string // only set while rendering per-environment files

	// SLO settings, derived by WithSLO. Strings are pre-formatted for the rule templates.
	SLO       string // availability target in percent, e.g. "99.9"
	Budget    string // error budget as a ratio, e.g. "0.001"
	FastBurn  string // 14.4 x budget: page threshold
	SlowBurn  string // 6 x budget: ticket threshold
	ErrPerMin int    // failing requests per 1000 in the generated promtool test
	OkPerMin  int
}

// Scaffold is the rendered output: app files go in the app repo, manifests in the manifest repo.
type Scaffold struct {
	App       map[string]string // output path -> content
	Manifests map[string]string // output path (relative to the manifests dir) -> content
}

// SupportedLanguages lists the --lang values that have a built-in template.
var SupportedLanguages = []string{"go", "python", "node"}

// Template source -> output path.
var (
	appTemplates = map[string]map[string]string{
		"go": {
			"templates/go/main.go.tmpl":      "main.go",
			"templates/go/main_test.go.tmpl": "main_test.go",
			"templates/go/go.mod.tmpl":       "go.mod",
			"templates/go/Dockerfile.tmpl":   "Dockerfile",
		},
		"python": {
			"templates/python/main.py.tmpl":              "main.py",
			"templates/python/test_main.py.tmpl":         "test_main.py",
			"templates/python/requirements.txt.tmpl":     "requirements.txt",
			"templates/python/requirements-dev.txt.tmpl": "requirements-dev.txt",
			"templates/python/Dockerfile.tmpl":           "Dockerfile",
		},
		"node": {
			"templates/node/index.js.tmpl":      "index.js",
			"templates/node/index.test.js.tmpl": "index.test.js",
			"templates/node/package.json.tmpl":  "package.json",
			"templates/node/Dockerfile.tmpl":    "Dockerfile",
		},
	}
	ciTemplates = map[string]string{
		"templates/ci/ci.yml.tmpl": ".github/workflows/ci.yml",
	}
	// Kustomize layout: base/ is shared, overlays/<env>/ customises it. monitoring/ holds
	// files that are not part of the kustomization (dashboards and plain Prometheus rules).
	manifestTemplates = map[string]string{
		"templates/manifests/deployment.yaml.tmpl":         "base/deployment.yaml",
		"templates/manifests/service.yaml.tmpl":            "base/service.yaml",
		"templates/manifests/hpa.yaml.tmpl":                "base/hpa.yaml",
		"templates/manifests/pdb.yaml.tmpl":                "base/pdb.yaml",
		"templates/manifests/servicemonitor.yaml.tmpl":     "base/servicemonitor.yaml",
		"templates/kustomize/base-kustomization.yaml.tmpl": "base/kustomization.yaml",
		"templates/manifests/alerts.rules.yml.tmpl":        "monitoring/alerts.rules.yml",
		"templates/manifests/alerts.rules.test.yml.tmpl":   "monitoring/alerts.rules.test.yml",
		"templates/manifests/grafana-dashboard.json.tmpl":  "monitoring/grafana-dashboard.json",
	}
	overlayTemplates = map[string]string{ // env -> template
		"dev":     "templates/kustomize/overlay-dev.yaml.tmpl",
		"staging": "templates/kustomize/overlay-staging.yaml.tmpl",
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

// normalizeLanguage maps aliases to a supported language, or returns an error listing the options.
func normalizeLanguage(lang string) (string, error) {
	switch strings.ToLower(lang) {
	case "go", "golang":
		return "go", nil
	case "python", "py", "fastapi":
		return "python", nil
	case "node", "nodejs", "javascript", "js":
		return "node", nil
	}
	return "", fmt.Errorf("unsupported language %q: choose one of %s", lang, strings.Join(SupportedLanguages, ", "))
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

// DefaultSLO is the availability target (percent) used when --slo is not given.
const DefaultSLO = 99.9

func newScaffoldData(name, language, database string) ScaffoldData {
	d := ScaffoldData{Name: name, AlertPrefix: camelCase(name), Language: language, Database: database, Port: 8080}
	d, _ = d.WithSLO(DefaultSLO)
	return d
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'g', 6, 64) }

// WithSLO fills the SLO fields for an availability target in percent (90 to 99.999).
func (d ScaffoldData) WithSLO(slo float64) (ScaffoldData, error) {
	if slo < 90 || slo >= 100 {
		return d, fmt.Errorf("invalid SLO %v: use a percentage from 90 to 99.999", slo)
	}
	budget := (100 - slo) / 100
	fast := 14.4 * budget
	// The promtool test sends 1000 requests/min; fail twice the page threshold (at least 1, at most all).
	errs := int(math.Ceil(fast * 2 * 1000))
	errs = min(max(errs, 1), 1000)
	d.SLO, d.Budget = fmtNum(slo), fmtNum(budget)
	d.FastBurn, d.SlowBurn = fmtNum(fast), fmtNum(6*budget)
	d.ErrPerMin, d.OkPerMin = errs, 1000-errs
	return d, nil
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

// buildScaffold renders the application, CI pipeline and Kubernetes/observability manifests
// for a supported language ("go", "python" or "node").
func buildScaffold(data ScaffoldData) (*Scaffold, error) {
	set, ok := appTemplates[data.Language]
	if !ok {
		return nil, fmt.Errorf("no template for language %q", data.Language)
	}
	s := &Scaffold{}

	var err error
	if s.App, err = renderSet(set, data); err != nil {
		return nil, err
	}
	ci, err := renderSet(ciTemplates, data)
	if err != nil {
		return nil, err
	}
	for k, v := range ci {
		s.App[k] = v
	}

	if s.Manifests, err = renderSet(manifestTemplates, data); err != nil {
		return nil, err
	}
	s.Manifests["base/prometheusrule.yaml"] = prometheusRuleManifest(data.Name, s.Manifests["monitoring/alerts.rules.yml"])

	for env, src := range overlayTemplates {
		envData := data
		envData.Env = env
		kustomization, err := renderTemplate(src, envData)
		if err != nil {
			return nil, err
		}
		namespace, err := renderTemplate("templates/kustomize/namespace.yaml.tmpl", envData)
		if err != nil {
			return nil, err
		}
		s.Manifests["overlays/"+env+"/kustomization.yaml"] = kustomization
		s.Manifests["overlays/"+env+"/namespace.yaml"] = namespace
	}
	return s, nil
}
