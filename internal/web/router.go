package web

import (
	"html/template"
	"net/http"
	"strings"
)

// PageFunc serves one kind of page for the parsed reference.
type PageFunc func(w http.ResponseWriter, r *http.Request, ref Ref)

// Handlers are the routes New dispatches to. A nil field answers 404.
type Handlers struct {
	Catalog PageFunc
	Repo    PageFunc
	Tag     PageFunc
	Digest  PageFunc
	// Static serves /-/static/. It sees the path below that, e.g. "/app.3f2a.css".
	Static http.Handler
}

// New returns the UI's root handler. base is the normalised base path,
// "/" or e.g. "/registry/"; every route lives below it. Paths under /-/ are
// UI-internal; anything else must parse as an image reference or gets a 404.
func New(base string, h Handlers) http.Handler {
	static := http.NotFoundHandler()
	if h.Static != nil {
		static = http.StripPrefix(base+"-/static", h.Static)
	}
	pages := map[Kind]PageFunc{KindCatalog: h.Catalog, KindRepo: h.Repo, KindTag: h.Tag, KindDigest: h.Digest}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := strings.CutPrefix(r.URL.Path, base)
		if !ok {
			if r.URL.Path+"/" == base {
				target := base
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, target, http.StatusMovedPermanently)
				return
			}
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasPrefix(p, "-/static/"):
			static.ServeHTTP(w, r)
		case strings.HasPrefix(p, "-/"):
			http.NotFound(w, r)
		default:
			ref, ok := parseRef(p)
			page := pages[ref.Kind]
			if !ok || page == nil {
				http.NotFound(w, r)
				return
			}
			// The same URL answers with a full page or a fragment.
			w.Header().Add("Vary", "HX-Request")
			page(w, r, ref)
		}
	})
}

// Fragment returns the partial an htmx request asks for with ?fragment=,
// or "" when the full page should be rendered.
func Fragment(r *http.Request) string {
	if r.Header.Get("HX-Request") != "true" {
		return ""
	}
	return r.URL.Query().Get("fragment")
}

// FuncMap returns the template URL helpers, all prefixed with base. hashed
// maps a static file's logical name to its served name; nil keeps the name.
func FuncMap(base string, hashed func(string) string) template.FuncMap {
	if hashed == nil {
		hashed = func(name string) string { return name }
	}
	return template.FuncMap{
		"catalogURL": func() string { return base },
		"repoURL":    func(repo string) string { return base + repo },
		"tagURL":     func(repo, tag string) string { return base + repo + ":" + tag },
		"digestURL":  func(repo, digest string) string { return base + repo + "@" + digest },
		"staticURL":  func(name string) string { return base + "-/static/" + hashed(name) },
	}
}
