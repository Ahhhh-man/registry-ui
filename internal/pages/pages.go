// Package pages holds the UI's page handlers. Each reads the registry through
// a Source, picks a template and renders it inside the layout; htmx fragment
// requests get the fragment template alone.
package pages

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"net/url"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/session"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// Server serves the pages. Its methods are web.PageFuncs.
type Server struct {
	Src       registry.Source
	Templates func() (*template.Template, error) // as returned by ui.Templates
	Title     string                             // registry title, shown in the header
	PullHost  string                             // host[:port] shown in docker pull commands
	BasePath  string                             // normalised base path, "/" or e.g. "/registry/"
	// Storage is set in storage mode, where a platform missing from an
	// index is one nobody has pulled through the cache yet.
	Storage bool
	// TagSort is the order a repository's tags are in when the URL has no
	// ?sort=, one of the tag sorts' names; "" means "version". TagsPerPage
	// is how many tags a page lists; 0 means 100.
	TagSort     string
	TagsPerPage int

	// Sessions and Check enable /-/login and /-/logout, and signing pages'
	// registry requests in as the session's user. Check asks the registry,
	// uncached, whether it accepts the credentials in ctx.
	Sessions *session.Sessions
	Check    func(ctx context.Context) error
	// Challenged reports whether the registry has asked an anonymous
	// request for credentials; until then the header offers no sign-in.
	Challenged func() bool
}

// Handlers returns the router's page handlers.
func (s *Server) Handlers() web.Handlers {
	h := web.Handlers{Catalog: s.Catalog, Repo: s.Repo, Tag: s.Tag, Digest: s.Digest}
	if s.Sessions != nil && s.Check != nil {
		h.Login, h.Logout = s.Login, s.Logout
		h.Catalog, h.Repo, h.Tag, h.Digest = s.auth(h.Catalog), s.auth(h.Repo), s.auth(h.Tag), s.auth(h.Digest)
	}
	return h
}

// auth wraps a page so its registry requests sign in with the session's
// credentials, or go anonymous without a session, and so web.Error can send
// a registry 401 to the sign-in page.
func (s *Server) auth(page web.PageFunc) web.PageFunc {
	return func(w http.ResponseWriter, r *http.Request, ref web.Ref) {
		user, pass, ok := s.Sessions.Get(r)
		if ok {
			r = r.WithContext(registry.WithCredentials(r.Context(), user, pass))
		}
		page(w, web.WithAuth(r, web.Auth{Login: s.BasePath + "-/login", SignedIn: ok, Catalog: ref.Kind == web.KindCatalog}), ref)
	}
}

// Header returns the layout data every page shares: the registry title and
// who is signed in, or a sign-in link once the registry has asked for
// credentials. Nothing about sign-in shows when the UI has none.
func (s *Server) Header(r *http.Request) ui.Page {
	p := ui.Page{Title: s.Title}
	if s.Sessions == nil || s.Check == nil {
		return p
	}
	p.User, _, _ = s.Sessions.Get(r)
	if p.User == "" && s.Challenged != nil && s.Challenged() {
		p.Login = s.BasePath + "-/login?next=" + url.QueryEscape(r.URL.RequestURI())
	}
	return p
}

// page renders the template name with data inside the layout, and
// name+"-band", if defined, as the layout's header band. p carries the
// page's title and search query; Title and Theme are filled in here.
func (s *Server) page(w http.ResponseWriter, r *http.Request, p ui.Page, name string, data any) {
	t, err := s.Templates()
	if err != nil {
		web.Error(w, r, err)
		return
	}
	var b bytes.Buffer
	if band := t.Lookup(name + "-band"); band != nil {
		if err := band.Execute(&b, data); err != nil {
			web.Error(w, r, err)
			return
		}
		p.Band = template.HTML(b.String()) //nolint:gosec // our own rendered template
		b.Reset()
	}
	if err := t.ExecuteTemplate(&b, name, data); err != nil {
		web.Error(w, r, err)
		return
	}
	h := s.Header(r)
	p.Title, p.Theme, p.User, p.Login = h.Title, ui.Theme(r), h.User, h.Login
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
