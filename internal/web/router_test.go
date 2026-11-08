package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const dgst = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// testRouter answers every page with "<kind> <repo> <tag> <digest>" and
// static files with "static <path>".
func testRouter(base string) http.Handler {
	page := func(w http.ResponseWriter, _ *http.Request, ref Ref) {
		_, _ = fmt.Fprintf(w, "%d %s %s %s", ref.Kind, ref.Repo, ref.Tag, ref.Digest)
	}
	return New(base, Handlers{
		Catalog: page, Repo: page, Tag: page, Digest: page,
		Static: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, "static %s", r.URL.Path)
		}),
	})
}

func TestRouter(t *testing.T) {
	tests := []struct {
		path string // below the base path
		want string // response body; "" means 404
	}{
		// Catalog.
		{"", "0   "},

		// Repositories, single and nested namespaces.
		{"nginx", "1 nginx  "},
		{"library/nginx", "1 library/nginx  "},
		{"a/b/c", "1 a/b/c  "},
		{"my-org/my_app.web", "1 my-org/my_app.web  "},
		{"a__b/c--d", "1 a__b/c--d  "},

		// Tags, including ".", "-" and "_".
		{"library/nginx:1.27.3", "2 library/nginx 1.27.3 "},
		{"a/b/c:v1.0-rc_1", "2 a/b/c v1.0-rc_1 "},
		{"nginx:_latest", "2 nginx _latest "},
		{"nginx:" + strings.Repeat("a", 128), "2 nginx " + strings.Repeat("a", 128) + " "},

		// Digests.
		{"library/nginx@" + dgst, "3 library/nginx  " + dgst},
		{"a/b/c@" + dgst, "3 a/b/c  " + dgst},

		// Internal routes.
		{"-/static/app.3f2a.css", "static /app.3f2a.css"},
		{"-/static/fonts/inter.woff2", "static /fonts/inter.woff2"},
		{"-/login", ""},
		{"-/static", ""},
		{"-/unknown", ""},

		// Invalid names.
		{"Library/nginx", ""},
		{"library/Nginx:1.0", ""},
		{"nginx:Latest-OK", "2 nginx Latest-OK "}, // tags may be upper case
		{"library/nginx/", ""},
		{"library/nginx:1.0/", ""},
		{"library//nginx", ""},
		{"/library/nginx", ""},
		{"-nginx", ""},
		{"nginx-", ""},
		{"a/.b", ""},
		{"a_-b", ""},
		{"a___b", ""},
		{"ng%20inx", ""},
		{"nginx:", ""},
		{"nginx:.1", ""},
		{"nginx:-1", ""},
		{"nginx:" + strings.Repeat("a", 129), ""},
		{"nginx:1:2", ""},
		{"nginx:1.0@" + dgst, ""},
		{"nginx@", ""},
		{"nginx@sha256:abc", ""},
		{"nginx@sha256:" + strings.Repeat("A", 64), ""},
		{"nginx@sha512:" + strings.Repeat("a", 128), ""},
		{"@" + dgst, ""},
		{":1.0", ""},
		{"a:b/c", ""},
	}
	for _, base := range []string{"/", "/registry/"} {
		h := testRouter(base)
		for _, tt := range tests {
			t.Run(base+tt.path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+tt.path, nil))
				if tt.want == "" {
					if rec.Code != http.StatusNotFound {
						t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body)
					}
					return
				}
				if rec.Code != http.StatusOK || rec.Body.String() != tt.want {
					t.Fatalf("got %d %q, want 200 %q", rec.Code, rec.Body, tt.want)
				}
			})
		}
	}
}

func TestRouterOutsideBase(t *testing.T) {
	h := testRouter("/registry/")
	tests := []struct {
		target   string
		code     int
		location string
	}{
		{"/", http.StatusNotFound, ""},
		{"/library/nginx", http.StatusNotFound, ""},
		{"/registryx/nginx", http.StatusNotFound, ""},
		{"/-/static/app.css", http.StatusNotFound, ""},
		{"/registry", http.StatusMovedPermanently, "/registry/"},
		{"/registry?q=nginx", http.StatusMovedPermanently, "/registry/?q=nginx"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))
		if rec.Code != tt.code || rec.Header().Get("Location") != tt.location {
			t.Errorf("%s: got %d %q, want %d %q", tt.target, rec.Code, rec.Header().Get("Location"), tt.code, tt.location)
		}
	}
}

func TestRouterNilHandlers(t *testing.T) {
	h := New("/", Handlers{})
	for _, target := range []string{"/", "/nginx", "/nginx:1", "/nginx@" + dgst, "/-/static/app.css"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, rec.Code)
		}
	}
}

func TestFragment(t *testing.T) {
	tests := []struct {
		hx, target, want string
	}{
		{"true", "/nginx?fragment=row", "row"},
		{"true", "/nginx", ""},
		{"", "/nginx?fragment=row", ""},
		{"false", "/nginx?fragment=row", ""},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, tt.target, nil)
		if tt.hx != "" {
			r.Header.Set("HX-Request", tt.hx)
		}
		if got := Fragment(r); got != tt.want {
			t.Errorf("HX-Request=%q %s: got %q, want %q", tt.hx, tt.target, got, tt.want)
		}
	}
}

func TestFuncMap(t *testing.T) {
	hashed := func(name string) string { return strings.Replace(name, ".", ".3f2a.", 1) }
	for _, tt := range []struct {
		base   string
		hashed func(string) string
		want   []string
	}{
		{"/", nil, []string{"/a/b", "/a/b:1.0", "/a/b@" + dgst, "/-/static/app.css"}},
		{"/registry/", hashed, []string{"/registry/a/b", "/registry/a/b:1.0", "/registry/a/b@" + dgst, "/registry/-/static/app.3f2a.css"}},
	} {
		fm := FuncMap(tt.base, tt.hashed)
		got := []string{
			fm["repoURL"].(func(string) string)("a/b"),
			fm["tagURL"].(func(string, string) string)("a/b", "1.0"),
			fm["digestURL"].(func(string, string) string)("a/b", dgst),
			fm["staticURL"].(func(string) string)("app.css"),
		}
		if fmt.Sprint(got) != fmt.Sprint(tt.want) {
			t.Errorf("base %q: got %v, want %v", tt.base, got, tt.want)
		}
		// Every page URL the helpers build must route.
		h := testRouter(tt.base)
		for _, u := range got[:3] {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("%s: status = %d, want 200", u, rec.Code)
			}
		}
	}
}
