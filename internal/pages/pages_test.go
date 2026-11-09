package pages

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/registry/fake"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// newTestServer returns a Server over the fake registry with the real
// templates and template funcs, served at base.
func newTestServer(t *testing.T, base string) *Server {
	t.Helper()
	funcs := web.FuncMap(base, ui.StaticName)
	maps.Copy(funcs, format.Funcs())
	tmpl, err := ui.Templates(funcs)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Src: fake.New(), Templates: tmpl, Title: "registry.example.com", PullHost: "registry.example.com", BasePath: base}
}

func TestPageWrapsLayoutAndFragmentDoesNot(t *testing.T) {
	s := newTestServer(t, "/")
	data := web.ErrorPage{Title: "Sample title", Message: "Sample message"}

	rec := httptest.NewRecorder()
	s.page(rec, httptest.NewRequest("GET", "/", nil), ui.Page{PageTitle: "Sample"}, "error", data)
	body := rec.Body.String()
	for _, want := range []string{"<!doctype html>", "<title>Sample · registry.example.com</title>", "Sample message"} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}

	rec = httptest.NewRecorder()
	s.fragment(rec, httptest.NewRequest("GET", "/", nil), "error-inline", data)
	if body := rec.Body.String(); strings.Contains(body, "<!doctype html>") || !strings.Contains(body, "Sample title") {
		t.Errorf("fragment should be the bare template, got:\n%s", body)
	}
}

// The footer's version reaches normal pages, the sign-in page and error pages.
func TestFooterVersionOnEveryPage(t *testing.T) {
	s := newLoginServer(t, "/")
	s.Version = "0.7.0"
	pages := map[string]func(w http.ResponseWriter, r *http.Request){
		"catalog": func(w http.ResponseWriter, r *http.Request) { s.Catalog(w, r, web.Ref{Kind: web.KindCatalog}) },
		"sign-in": s.Login,
		"error": func(w http.ResponseWriter, r *http.Request) {
			ui.ErrorRenderer(s.Templates, s.Header)(w, r, web.ErrorPage{Status: http.StatusNotFound, Title: "Not found"})
		},
	}
	for name, page := range pages {
		rec := httptest.NewRecorder()
		page(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		_, footer, _ := strings.Cut(rec.Body.String(), "<footer")
		if !strings.Contains(footer, `/releases/tag/v0.7.0"`) || !strings.Contains(footer, "Report an issue") {
			t.Errorf("%s: no footer with the version:\n%s", name, rec.Body)
		}
	}
}
