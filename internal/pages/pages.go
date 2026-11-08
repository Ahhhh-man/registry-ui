// Package pages holds the UI's page handlers. Each reads the registry through
// a Source, picks a template and renders it inside the layout; htmx fragment
// requests get the fragment template alone.
package pages

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// Server serves the pages. Its methods are web.PageFuncs.
type Server struct {
	Src       registry.Source
	Templates func() (*template.Template, error) // as returned by ui.Templates
	Title     string                             // registry title, shown in the header
	PullHost  string                             // host[:port] shown in docker pull commands
}

// Handlers returns the router's page handlers.
func (s *Server) Handlers() web.Handlers {
	return web.Handlers{Catalog: s.Catalog, Repo: s.Repo, Tag: s.Tag, Digest: s.Digest}
}

// page renders the template name with data inside the layout. p carries the
// page's title and search query; Title and Theme are filled in here.
func (s *Server) page(w http.ResponseWriter, r *http.Request, p ui.Page, name string, data any) {
	t, err := s.Templates()
	if err != nil {
		web.Error(w, r, err)
		return
	}
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, name, data); err != nil {
		web.Error(w, r, err)
		return
	}
	p.Title, p.Theme = s.Title, ui.Theme(r)
	p.Body = template.HTML(b.String()) //nolint:gosec // our own rendered template
	b.Reset()
	if err := t.ExecuteTemplate(&b, "layout", p); err != nil {
		web.Error(w, r, err)
		return
	}
	write(w, b.Bytes())
}

// fragment renders the template name with data on its own, for htmx.
func (s *Server) fragment(w http.ResponseWriter, r *http.Request, name string, data any) {
	t, err := s.Templates()
	if err != nil {
		web.Error(w, r, err)
		return
	}
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, name, data); err != nil {
		web.Error(w, r, err)
		return
	}
	write(w, b.Bytes())
}

func write(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

// notYet answers pages that don't exist yet.
func notYet(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "this page isn't built yet", http.StatusNotImplemented)
}
