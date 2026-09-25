package server

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/rag"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
	"github.com/chetanmeniya431-ai/resume-screener-go/web"
)

var funcs = template.FuncMap{
	"join": func(s []string) string { return strings.Join(s, ", ") },
	"pct": func(part, total int) int {
		if total == 0 {
			return 0
		}
		return part * 100 / total
	},
	"scoreClass": func(score int) string {
		switch {
		case score >= 75:
			return "bg-emerald-50 text-emerald-700 ring-emerald-600/20"
		case score >= 50:
			return "bg-amber-50 text-amber-700 ring-amber-600/20"
		default:
			return "bg-gray-100 text-gray-600 ring-gray-500/20"
		}
	},
	"statusClass": func(st string) string {
		switch st {
		case store.StatusDone:
			return "bg-emerald-50 text-emerald-700"
		case store.StatusProcessing:
			return "bg-teal-50 text-teal-700"
		case store.StatusQueued:
			return "bg-sky-50 text-sky-700"
		case store.StatusFailed:
			return "bg-rose-50 text-rose-700"
		default:
			return "bg-gray-100 text-gray-600"
		}
	},
	"ago": func(t time.Time) string {
		d := time.Since(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%d min ago", int(d.Minutes()))
		case d < 24*time.Hour:
			return fmt.Sprintf("%d h ago", int(d.Hours()))
		}
		return t.Format("2 Jan 2006")
	},
	"f1":   func(v float64) string { return fmt.Sprintf("%.1f", v) },
	"add":  func(a, b int) int { return a + b },
	"barW": func(v, maxV float64) int { return int(v / maxV * 100) },
	"secs": func(ms int) float64 { return float64(ms) / 1000 },
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
}

var pageNames = []string{"home", "job", "candidate", "pipeline"}

func loadPages() (map[string]*template.Template, error) {
	pages := map[string]*template.Template{}
	for _, name := range pageNames {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(web.FS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
		pages[name] = t
	}
	return pages, nil
}

// render writes to a buffer first, so a template error never sends half a page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	data["Page"] = name
	data["ChatModel"] = s.opts.ChatModel
	data["EmbedModel"] = s.opts.EmbedModel
	var buf bytes.Buffer
	if err := s.pages[name].Execute(&buf, data); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func urlEscape(s string) string { return url.QueryEscape(s) }

// pipelineDiverse finds the best-matching chunks, at most 1 per candidate, 6 in total.
// Fewer, more varied extracts keep the prompt short, which matters a lot on a CPU-only server.
func pipelineDiverse(chunks []store.Chunk, q []float32) []rag.Hit {
	return rag.Diverse(rag.Search(chunks, q, 30), 1, 6)
}
