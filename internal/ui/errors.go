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
// returns; header gives the layout's title and sign-in state for r.
func ErrorRenderer(templates func() (*template.Template, error), header func(r *http.Request) Page) web.ErrorRenderer {
	return func(w http.ResponseWriter, r *http.Request, p web.ErrorPage) {
		var b bytes.Buffer
		t, err := templates()
		if err == nil && p.Inline {
			err = t.ExecuteTemplate(&b, "error-inline", p)
		} else if err == nil {
			if err = t.ExecuteTemplate(&b, "error", p); err == nil {
				page := header(r)
				page.PageTitle, page.Theme = p.Title, Theme(r)
				page.Body = template.HTML(b.String()) //nolint:gosec // our own rendered template
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
