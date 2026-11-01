package main

import (
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
)

// The pages are read-only and use html/template, which escapes every value (RCA text comes from an LLM).
var pageTemplates = template.Must(template.New("pages").Parse(`
{{define "style"}}<style>
 body{font:15px/1.5 system-ui,sans-serif;max-width:1000px;margin:2rem auto;padding:0 1rem;color:#1b1f24}
 table{border-collapse:collapse;width:100%}th,td{text-align:left;padding:.5rem .6rem;border-bottom:1px solid #d8dee4}
 .pill{padding:.1rem .6rem;border-radius:1rem;font-size:.85em}
 .open{background:#ffebe9;color:#a40e26}.acknowledged{background:#fff8c5;color:#7d4e00}.resolved{background:#dafbe1;color:#116329}
 pre{white-space:pre-wrap;background:#f6f8fa;padding:1rem;border-radius:6px}a{color:#0969da}
 @media (prefers-color-scheme:dark){body{background:#0d1117;color:#e6edf3}th,td{border-color:#30363d}pre{background:#161b22}a{color:#58a6ff}}
</style>{{end}}
{{define "list"}}<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Incidents</title>{{template "style"}}
<h1>Incidents</h1>
{{if .}}<table><tr><th>#</th><th>Subject</th><th>Status</th><th>Alerts</th><th>Opened (UTC)</th></tr>
{{range .}}<tr><td><a href="/incidents/{{.ID}}">{{.ID}}</a></td><td>{{.Subject}}</td><td><span class="pill {{.Status}}">{{.Status}}</span></td><td>{{.AlertCount}}</td><td>{{.CreatedAt.Format "2006-01-02 15:04:05"}}</td></tr>{{end}}</table>
{{else}}<p>No incidents yet.</p>{{end}}{{end}}
{{define "detail"}}<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Incident {{.ID}}</title>{{template "style"}}
<p><a href="/incidents">&larr; All incidents</a></p>
<h1>Incident #{{.ID}}: {{.Subject}}</h1>
<p><span class="pill {{.Status}}">{{.Status}}</span>{{if .AckedBy}} acknowledged by {{.AckedBy}}{{end}}</p>
<ul><li>Namespace: {{.Namespace}}</li><li>Alerts: {{.Alertnames}} ({{.AlertCount}} received)</li><li>Pods: {{.Pods}}</li>
<li>Opened {{.CreatedAt.Format "2006-01-02 15:04:05"}} UTC, last update {{.UpdatedAt.Format "2006-01-02 15:04:05"}} UTC</li></ul>
<h2>Root cause analysis</h2>{{if .RCA}}<pre>{{.RCA}}</pre>{{else}}<p>The analysis is still running.</p>{{end}}{{end}}
`))

func (a *App) incidentsPage(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.List(50)
	if err != nil {
		slog.Error("list incidents failed", "error", err.Error())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTemplates.ExecuteTemplate(w, "list", list)
}

func (a *App) incidentPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	inc, err := a.store.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTemplates.ExecuteTemplate(w, "detail", inc)
}
