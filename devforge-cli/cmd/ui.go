package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// PortalConfig says where the portal finds its data. Everything except the catalog is optional:
// a missing source shows a note on the page instead of failing it.
type PortalConfig struct {
	WebhookURL    string // incidents feed: <url>/api/incidents.json (as the portal itself reaches it)
	WebhookPublic string // the same service as a browser reaches it; used for page links
	PrometheusURL string // live status of each service
	GrafanaURL    string
	ArgoCDURL     string
}

func portalConfigFromEnv() PortalConfig {
	trim := func(k, d string) string { return strings.TrimRight(envOr(k, d), "/") }
	webhook := trim("DEVFORGE_WEBHOOK_URL", "http://localhost:9090")
	return PortalConfig{
		WebhookURL:    webhook,
		WebhookPublic: trim("DEVFORGE_WEBHOOK_PUBLIC_URL", webhook),
		PrometheusURL: trim("PROMETHEUS_URL", ""),
		GrafanaURL:    trim("GRAFANA_URL", "http://localhost:3000"),
		ArgoCDURL:     trim("ARGOCD_URL", ""),
	}
}

type ServiceView struct {
	CatalogEntry
	Status       string `json:"status"`      // "2/2 pods up", "no metrics yet", "status unavailable"
	StatusClass  string `json:"statusClass"` // up, down, unknown
	DashboardURL string `json:"dashboardUrl,omitempty"`
	ArgoURL      string `json:"argoUrl,omitempty"`
}

type IncidentView struct {
	ID             int64    `json:"id"`
	Subject        string   `json:"subject"`
	Namespace      string   `json:"namespace"`
	Status         string   `json:"status"`
	AlertCount     int      `json:"alertCount"`
	Pods           []string `json:"pods"`
	PendingActions int      `json:"pendingActions"`
	CreatedAt      string   `json:"createdAt"`
	URL            string   `json:"url"`
}

type PortalData struct {
	Services  []ServiceView
	Incidents []IncidentView
	Notes     []string // problems reaching data sources
	Generated string
	Links     PortalConfig
}

var portalHTTP = &http.Client{Timeout: 4 * time.Second}

func getJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := portalHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d", rawURL, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// promScalar runs an instant query and returns the first value.
func promScalar(ctx context.Context, base, query string) (float64, bool, error) {
	var res struct {
		Data struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := getJSON(ctx, base+"/api/v1/query?query="+url.QueryEscape(query), &res); err != nil {
		return 0, false, err
	}
	if len(res.Data.Result) == 0 {
		return 0, false, nil
	}
	f, err := strconv.ParseFloat(fmt.Sprint(res.Data.Result[0].Value[1]), 64)
	return f, err == nil, err
}

// serviceStatus reads the `up` metric the generated Prometheus scrape config exposes for the service.
func serviceStatus(ctx context.Context, cfg PortalConfig, name string) (text, class string) {
	if cfg.PrometheusURL == "" {
		return "Prometheus not configured", "unknown"
	}
	sel := fmt.Sprintf(`{job=%q}`, name)
	up, haveUp, err1 := promScalar(ctx, cfg.PrometheusURL, "sum(up"+sel+")")
	total, haveTotal, err2 := promScalar(ctx, cfg.PrometheusURL, "count(up"+sel+")")
	switch {
	case err1 != nil || err2 != nil:
		return "status unavailable", "unknown"
	case !haveUp || !haveTotal || total == 0:
		return "no metrics yet", "unknown"
	case up == total:
		return fmt.Sprintf("%d/%d pods up", int(up), int(total)), "up"
	}
	return fmt.Sprintf("%d/%d pods up", int(up), int(total)), "down"
}

// buildPortalData gathers the catalog, each service's live status, and recent incidents.
func buildPortalData(ctx context.Context, cfg PortalConfig) PortalData {
	data := PortalData{Links: cfg, Generated: time.Now().UTC().Format("15:04:05 UTC")}

	entries, err := loadCatalog()
	if err != nil {
		data.Notes = append(data.Notes, "Service catalog: "+err.Error())
	}
	for _, e := range entries {
		v := ServiceView{CatalogEntry: e}
		v.Status, v.StatusClass = serviceStatus(ctx, cfg, e.Name)
		if cfg.GrafanaURL != "" {
			v.DashboardURL = cfg.GrafanaURL + "/d/" + e.Name + "-overview"
		}
		if cfg.ArgoCDURL != "" {
			v.ArgoURL = cfg.ArgoCDURL + "/applications/argocd/" + e.Name + "-dev"
		}
		data.Services = append(data.Services, v)
	}

	if cfg.WebhookURL == "" {
		data.Notes = append(data.Notes, "Incidents: DEVFORGE_WEBHOOK_URL is not set")
	} else if err := getJSON(ctx, cfg.WebhookURL+"/api/incidents.json", &data.Incidents); err != nil {
		data.Notes = append(data.Notes, "Incidents unavailable: "+err.Error())
	}
	return data
}

var portalTemplate = template.Must(template.New("portal").Parse(`<!doctype html><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="15">
<title>DevForge portal</title>
<style>
 body{font:15px/1.5 system-ui,sans-serif;max-width:1100px;margin:2rem auto;padding:0 1rem;color:#1b1f24}
 table{border-collapse:collapse;width:100%;margin-bottom:1.5rem}th,td{text-align:left;padding:.5rem .6rem;border-bottom:1px solid #d8dee4;vertical-align:top}
 .pill{padding:.1rem .6rem;border-radius:1rem;font-size:.85em;white-space:nowrap}
 .up,.resolved{background:#dafbe1;color:#116329}.down,.open{background:#ffebe9;color:#a40e26}.unknown,.acknowledged{background:#fff8c5;color:#7d4e00}
 .note{background:#fff8c5;color:#7d4e00;padding:.5rem .8rem;border-radius:6px;margin:.4rem 0}a{color:#0969da}small{color:#656d76}
 @media (prefers-color-scheme:dark){body{background:#0d1117;color:#e6edf3}th,td{border-color:#30363d}a{color:#58a6ff}small{color:#8b949e}}
</style>
<h1>DevForge portal</h1>
<p><small>Updated {{.Generated}} - refreshes every 15 seconds.</small>
{{if .Links.GrafanaURL}} <a href="{{.Links.GrafanaURL}}">Grafana</a>{{end}}
{{if .Links.ArgoCDURL}} &middot; <a href="{{.Links.ArgoCDURL}}">Argo CD</a>{{end}}
{{if .Links.WebhookPublic}} &middot; <a href="{{.Links.WebhookPublic}}/incidents">Incidents</a> &middot; <a href="{{.Links.WebhookPublic}}/audit">Audit log</a>{{end}}</p>
{{range .Notes}}<div class="note">{{.}}</div>{{end}}
<h2>Services</h2>
{{if .Services}}<table><tr><th>Name</th><th>Language</th><th>Database</th><th>Status</th><th>Created</th><th>Links</th></tr>
{{range .Services}}<tr><td><strong>{{.Name}}</strong><br><small>{{.Path}}</small></td><td>{{.Language}}</td><td>{{.Database}}</td>
<td><span class="pill {{.StatusClass}}">{{.Status}}</span></td><td>{{.CreatedAt.Format "2006-01-02 15:04"}}</td>
<td>{{if .DashboardURL}}<a href="{{.DashboardURL}}">dashboard</a> {{end}}{{if .AppRepo}}<a href="{{.AppRepo}}">app repo</a> {{end}}{{if .ManifestRepo}}<a href="{{.ManifestRepo}}">manifests</a> {{end}}{{if .ArgoURL}}<a href="{{.ArgoURL}}">argo cd</a>{{end}}</td></tr>{{end}}</table>
{{else}}<p>No services yet. Create one with <code>devforge create --name my-service</code>.</p>{{end}}
<h2>Incidents</h2>
{{if .Incidents}}<table><tr><th>#</th><th>Subject</th><th>Status</th><th>Alerts</th><th>Waiting for approval</th><th>Opened (UTC)</th></tr>
{{range .Incidents}}<tr><td><a href="{{.URL}}">{{.ID}}</a></td><td>{{.Subject}}<br><small>{{.Namespace}}</small></td><td><span class="pill {{.Status}}">{{.Status}}</span></td><td>{{.AlertCount}}</td>
<td>{{if .PendingActions}}<strong>{{.PendingActions}} action{{if ne .PendingActions 1}}s{{end}}</strong>{{else}}-{{end}}</td><td>{{.CreatedAt}}</td></tr>{{end}}</table>
{{else}}<p>No incidents.</p>{{end}}`))

func portalHandler(cfg PortalConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := portalTemplate.Execute(w, buildPortalData(ctx, cfg)); err != nil {
			fmt.Fprintln(os.Stderr, "portal: render failed:", err)
		}
	})
	mux.HandleFunc("GET /api/services", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		d := buildPortalData(ctx, cfg)
		if d.Services == nil {
			d.Services = []ServiceView{}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(d.Services); err != nil {
			fmt.Fprintln(os.Stderr, "portal: write failed:", err)
		}
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

var uiPort int

var uiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Serve a small web portal over the service catalog and incidents",
	Long: `Serves a read-only dashboard: every service created with DevForge (from the catalog file),
its live status from Prometheus, and recent incidents from the AI webhook.

Configure with DEVFORGE_WEBHOOK_URL, PROMETHEUS_URL, GRAFANA_URL and ARGOCD_URL.`,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := portalConfigFromEnv()
		addr := ":" + strconv.Itoa(uiPort)
		fmt.Printf("🧭 DevForge portal on %s (catalog %s, webhook %s)\n", addr, catalogPath(), cfg.WebhookURL)
		if err := http.ListenAndServe(addr, portalHandler(cfg)); err != nil {
			fmt.Println("❌ Error:", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(uiCmd)
	uiCmd.Flags().IntVar(&uiPort, "port", 8088, "Port to listen on")
}
