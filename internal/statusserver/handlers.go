package statusserver

import (
	"encoding/json"
	"html/template"
	"net/http"
	"time"
)

// Handler returns the mux serving /healthz, / and /status.json. The index
// page deliberately emits no links: it's reverse-proxied behind a reverse proxy's
// path-stripping rule, which strips the /gitops prefix before the agent ever sees
// the request, so an absolute or relative link generated here would point
// at the wrong place. A single link-free page sidesteps the problem
// entirely -- see README.md.
func (t *Tracker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", t.handleHealthz)
	mux.HandleFunc("/status.json", t.handleStatusJSON)
	mux.HandleFunc("/", t.handleIndex)
	return mux
}

func (t *Tracker) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s := t.Snapshot()
	if !s.Healthy() {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("unhealthy: " + s.LastCycleError + "\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok\n"))
}

func (t *Tracker) handleStatusJSON(w http.ResponseWriter, r *http.Request) {
	s := t.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (t *Tracker) handleIndex(w http.ResponseWriter, r *http.Request) {
	s := t.Snapshot()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTemplate.Execute(w, indexView{
		Status: s,
		Now:    time.Now(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type indexView struct {
	Status Status
	Now    time.Time
}

// ago renders a duration as "N minutes ago" style text. Zero times (never
// happened yet) render as "never" instead of a nonsense multi-decade
// duration.
func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := max(now.Sub(t).Round(time.Second), 0)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return d.Round(time.Second).String() + " ago"
	default:
		return d.Round(time.Minute).String() + " ago"
	}
}

func until(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := t.Sub(now).Round(time.Second)
	if d < 0 {
		return "any moment now"
	}
	return d.String()
}

var indexTemplate = template.Must(template.New("index").Funcs(template.FuncMap{
	"ago":   ago,
	"until": until,
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>gitops-agent status</title>
<style>
  body { font-family: system-ui, sans-serif; margin: 1rem; max-width: 40rem; color: #111; background: #fff; }
  h1 { font-size: 1.2rem; }
  h2 { font-size: 1rem; margin-top: 1.5rem; }
  table { width: 100%; border-collapse: collapse; }
  td, th { text-align: left; padding: 0.3rem 0.4rem; border-bottom: 1px solid #ddd; font-size: 0.9rem; }
  .ok { color: #0a0; font-weight: bold; }
  .bad { color: #c00; font-weight: bold; }
  .muted { color: #666; }
  .err { font-family: monospace; font-size: 0.8rem; color: #c00; word-break: break-word; }
  @media (prefers-color-scheme: dark) {
    body { color: #eee; background: #111; }
    td, th { border-bottom: 1px solid #333; }
    .muted { color: #999; }
  }
</style>
</head>
<body>
<h1>gitops-agent {{.Status.Version}}
  {{if .Status.Healthy}}<span class="ok">&#9679; healthy</span>{{else}}<span class="bad">&#9679; unhealthy</span>{{end}}
</h1>
<p class="muted">
  started {{ago .Status.StartedAt .Now}}<br>
  last cycle: {{ago .Status.LastCycleAt .Now}}
  {{if .Status.LastCycleError}}<br><span class="err">{{.Status.LastCycleError}}</span>{{end}}<br>
  next cycle in {{until .Status.NextCycleAt .Now}}{{if .Status.Active}} (active window){{end}}
</p>

<h2>sync</h2>
<p class="muted">
  last attempt {{ago .Status.LastSyncAttempt .Now}}<br>
  last success {{ago .Status.LastSyncSuccess .Now}}<br>
  {{if .Status.CommitHash}}commit <code>{{.Status.CommitHash}}</code><br>{{end}}
  {{if .Status.LastSyncError}}<span class="err">{{.Status.LastSyncError}}</span>{{end}}
</p>

<h2>services</h2>
<table>
<tr><th>service</th><th>state</th><th>last success</th><th>last attempt</th></tr>
{{range .Status.Services}}
<tr>
  <td>{{.Name}}{{if not .Enabled}} <span class="muted">(disabled)</span>{{end}}</td>
  <td>{{if .LastAttemptOK}}<span class="ok">ok</span>{{else if .LastError}}<span class="bad">error</span>{{else}}<span class="muted">pending</span>{{end}}</td>
  <td>{{ago .LastSuccess $.Now}}</td>
  <td>{{ago .LastAttempt $.Now}}</td>
</tr>
{{if .LastError}}<tr><td colspan="4" class="err">{{.LastError}}</td></tr>{{end}}
{{end}}
</table>
</body>
</html>
`))
