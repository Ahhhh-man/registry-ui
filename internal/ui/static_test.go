package ui

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

var css = strings.Repeat("body { color: red; }\n", 50)

func testAssets(t *testing.T) *assets {
	t.Helper()
	a, err := loadAssets(fstest.MapFS{
		"app.css":            {Data: []byte(css)},
		"app.js":             {Data: []byte(strings.Repeat("console.log(1);\n", 50))},
		"fonts/inter.woff2":  {Data: []byte("wOF2")},
		"icons/sprite.svg":   {Data: []byte("<svg></svg>")},
		"htmx.min.js":        {Data: []byte("var htmx")},
		"fonts/mono.v1.woff": {Data: []byte("x")},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func get(a *assets, name, acceptEncoding string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/"+name, nil)
	if acceptEncoding != "" {
		r.Header.Set("Accept-Encoding", acceptEncoding)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestHashedNameResolves(t *testing.T) {
	a := testAssets(t)
	name := a.hashed["app.css"]
	if !strings.HasPrefix(name, "app.") || !strings.HasSuffix(name, ".css") || len(name) != len("app.12345678.css") {
		t.Fatalf("hashed name = %q", name)
	}
	if got := a.hashed["htmx.min.js"]; !strings.HasPrefix(got, "htmx.min.") || !strings.HasSuffix(got, ".js") {
		t.Errorf("htmx hashed name = %q", got)
	}
	if got := a.hashed["fonts/inter.woff2"]; !strings.HasPrefix(got, "fonts/inter.") {
		t.Errorf("font hashed name = %q", got)
	}

	w := get(a, name, "")
	if w.Code != http.StatusOK || w.Body.String() != css {
		t.Fatalf("GET %s = %d %q", name, w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestUnknownOrStaleNameIs404(t *testing.T) {
	a := testAssets(t)
	for _, name := range []string{"app.css", "app.00000000.css", "missing.12345678.js", ""} {
		if w := get(a, name, ""); w.Code != http.StatusNotFound {
			t.Errorf("GET %q = %d, want 404", name, w.Code)
		}
	}
}

func TestGzipNegotiation(t *testing.T) {
	a := testAssets(t)
	name := a.hashed["app.css"]
	for _, tc := range []struct {
		accept string
		gzip   bool
	}{
		{"", false},
		{"gzip", true},
		{"br, gzip, deflate", true},
		{"GZIP;q=0.5", true},
		{"gzip;q=0", false},
		{"deflate, br", false},
	} {
		w := get(a, name, tc.accept)
		if got := w.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Errorf("%q: Vary = %q", tc.accept, got)
		}
		gzipped := w.Header().Get("Content-Encoding") == "gzip"
		if gzipped != tc.gzip {
			t.Errorf("%q: gzip = %v, want %v", tc.accept, gzipped, tc.gzip)
			continue
		}
		body := w.Body.Bytes()
		if gzipped {
			zr, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if body, err = io.ReadAll(zr); err != nil {
				t.Fatal(err)
			}
		}
		if string(body) != css {
			t.Errorf("%q: body differs from the original", tc.accept)
		}
	}

	// Already-compressed files aren't gzipped again.
	w := get(a, a.hashed["fonts/inter.woff2"], "gzip")
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != "wOF2" {
		t.Errorf("woff2 was gzipped: %v", w.Header())
	}
}

func TestContentType(t *testing.T) {
	a := testAssets(t)
	for name, want := range map[string]string{
		"app.css":            "text/css; charset=utf-8",
		"app.js":             "text/javascript; charset=utf-8",
		"fonts/inter.woff2":  "font/woff2",
		"icons/sprite.svg":   "image/svg+xml",
		"fonts/mono.v1.woff": "application/octet-stream",
	} {
		if got := get(a, a.hashed[name], "gzip").Header().Get("Content-Type"); got != want {
			t.Errorf("%s: Content-Type = %q, want %q", name, got, want)
		}
	}
}
