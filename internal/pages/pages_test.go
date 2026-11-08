package pages

import (
	"maps"
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
