//go:build !dev

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/web"
)

func TestErrorRenderer(t *testing.T) {
	tmpl, err := Templates(serverFuncs("/registry/", StaticName))
	if err != nil {
		t.Fatal(err)
	}
	render := ErrorRenderer(tmpl, "registry.example.com")
	p := web.ErrorPage{Status: http.StatusBadGateway, Title: "Can't reach the registry", Message: "Down.", Retry: "/registry/nginx?x=1&y=2"}

	for _, inline := range []bool{false, true} {
		p.Inline = inline
		r := httptest.NewRequest(http.MethodGet, "/registry/nginx", nil)
		r.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
		rec := httptest.NewRecorder()
		render(rec, r, p)
		s := rec.Body.String()
		if rec.Code != http.StatusBadGateway || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Errorf("inline=%t: got %d %q", inline, rec.Code, rec.Header().Get("Content-Type"))
		}
		for _, want := range []string{"Can&#39;t reach the registry", "Down.", `href="/registry/nginx?x=1&amp;y=2"`, "Try again"} {
			if !strings.Contains(s, want) {
				t.Errorf("inline=%t: lacks %s:\n%s", inline, want, s)
			}
		}
		if page := strings.Contains(s, `<html lang="en" class="dark">`) && strings.Contains(s, "<title>Can&#39;t reach the registry · registry.example.com</title>"); page == inline {
			t.Errorf("inline=%t: full page = %t:\n%s", inline, page, s)
		}
		if strings.Contains(s, "style=") || strings.Contains(s, "<style") {
			t.Errorf("inline=%t: has inline styles", inline)
		}
	}

	// Without a retry link, there's no "Try again".
	rec := httptest.NewRecorder()
	render(rec, httptest.NewRequest(http.MethodGet, "/", nil), web.ErrorPage{Status: http.StatusNotFound, Title: "Not found"})
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Try again") || !strings.Contains(rec.Body.String(), `href="/registry/"`) {
		t.Errorf("404: got %d\n%s", rec.Code, rec.Body)
	}
}
