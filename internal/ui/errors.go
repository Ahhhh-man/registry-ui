package ui

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// ErrorRenderer draws web's error states: the "error" template inside the
// layout, or "error-inline" alone for htmx. templates is what Templates
// returns; title is the registry title for the header.
func ErrorRenderer(templates func() (*template.Template, error), title string) web.ErrorRenderer {
	return func(w http.ResponseWriter, r *http.Request, p web.ErrorPage) {
		var b bytes.Buffer
		t, err := templates()
		if err == nil && p.Inline {
			err = t.ExecuteTemplate(&b, "error-inline", p)
		} else if err == nil {
			if err = t.ExecuteTemplate(&b, "error", p); err == nil {
				page := Page{Title: title, PageTitle: p.Title, Theme: Theme(r), Body: template.HTML(b.String())} //nolint:gosec // our own rendered template
				b.Reset()
				err = t.ExecuteTemplate(&b, "layout", page)
			}
		}
		if err != nil {
			slog.Error("rendering error page", "err", err)
			http.Error(w, p.Title, p.Status)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(p.Status)
		_, _ = w.Write(b.Bytes())
	}
}
