package web

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
)

// captureLog sends slog's default logger to a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &b
}

// recordRenderer renders an ErrorPage as "<status> <title> retry=<retry> inline=<inline>".
func recordRenderer(w http.ResponseWriter, _ *http.Request, p ErrorPage) {
	w.WriteHeader(p.Status)
	_, _ = fmt.Fprintf(w, "%d %s retry=%s inline=%t", p.Status, p.Title, p.Retry, p.Inline)
}

func TestSecurityHeaders(t *testing.T) {
	captureLog(t)
	h := Middleware(testRouter("/registry/"), recordRenderer)
	for _, tt := range []struct {
		name, target string
		hx           bool
		code         int
	}{
		{"page", "/registry/library/nginx", false, http.StatusOK},
		{"fragment", "/registry/library/nginx?fragment=row", true, http.StatusOK},
		{"static", "/registry/-/static/app.css", false, http.StatusOK},
		{"404", "/registry/Not/Valid", false, http.StatusNotFound},
		{"redirect", "/registry", false, http.StatusMovedPermanently},
	} {
		r := httptest.NewRequest(http.MethodGet, tt.target, nil)
		if tt.hx {
			r.Header.Set("HX-Request", "true")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != tt.code {
			t.Errorf("%s: status = %d, want %d", tt.name, rec.Code, tt.code)
		}
		for k, v := range map[string]string{
			"Content-Security-Policy": "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "same-origin",
		} {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", tt.name, k, got, v)
			}
		}
	}
}

func TestRequestLog(t *testing.T) {
	log := captureLog(t)
	h := Middleware(testRouter("/"), recordRenderer)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/library/nginx?q=x", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodHead, "/-/nope", nil))
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one line per request, got:\n%s", log)
	}
	for i, want := range []string{"msg=request method=GET path=/library/nginx status=200 duration=", "msg=request method=HEAD path=/-/nope status=404 duration="} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to contain %q", i, lines[i], want)
		}
	}
}

func TestPanic(t *testing.T) {
	log := captureLog(t)
	h := Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }), recordRenderer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "500 Something went wrong retry= inline=false" {
		t.Errorf("got %d %q", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("500 lacks the security headers")
	}
	if s := log.String(); !strings.Contains(s, "panic=boom") || !strings.Contains(s, "status=500") {
		t.Errorf("log lacks the panic or the request line:\n%s", s)
	}

	// Without a renderer, the 500 is plain text and still carries no stack.
	rec = httptest.NewRecorder()
	Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }), nil).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "goroutine") {
		t.Errorf("plain: got %d %q", rec.Code, rec.Body)
	}
}

func TestError(t *testing.T) {
	log := captureLog(t)
	upstream := &transport.Error{StatusCode: http.StatusServiceUnavailable}
	tests := []struct {
		name       string
		err        error
		hx         bool
		currentURL string
		want       string // the recordRenderer body
	}{
		{"not found", fmt.Errorf("repository x: %w", registry.ErrNotFound), false, "", "404 Not found retry= inline=false"},
		{"forbidden", fmt.Errorf("catalog: %w", registry.ErrForbidden), false, "", "403 No access retry= inline=false"},
		{"unauthorized", &registry.UnauthorizedError{Challenged: true}, false, "", "403 No access retry= inline=false"},
		{"unavailable", fmt.Errorf("catalog: %w: %w", registry.ErrUnavailable, upstream), false, "", "502 Can't reach the registry retry=/library/nginx?fragment=row inline=false"},
		{"unavailable inline", registry.ErrUnavailable, true, "http://ui.example/registry/?q=ng", "502 Can't reach the registry retry=/registry/?q=ng inline=true"},
		{"unavailable inline, foreign URL", registry.ErrUnavailable, true, "http://ui.example//evil.example/x", "502 Can't reach the registry retry=/library/nginx?fragment=row inline=true"},
		{"other", errors.New("template: boom"), true, "", "500 Something went wrong retry= inline=true"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/library/nginx?fragment=row", nil)
		if tt.hx {
			r.Header.Set("HX-Request", "true")
			r.Header.Set("HX-Current-URL", tt.currentURL)
		}
		rec := httptest.NewRecorder()
		Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { Error(w, r, tt.err) }), recordRenderer).ServeHTTP(rec, r)
		if rec.Body.String() != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, rec.Body, tt.want)
		}
	}
	if !strings.Contains(log.String(), "upstream_status=503") {
		t.Errorf("log lacks the upstream status:\n%s", log)
	}
}

func TestGzip(t *testing.T) {
	log := captureLog(t)
	big := strings.Repeat("<p>hello</p>", 200)
	for _, tt := range []struct {
		name, ctype, encoding, body string
		gzip                        bool
	}{
		{"big HTML", "text/html; charset=utf-8", "", big, true},
		{"small HTML", "text/html; charset=utf-8", "", big[:gzipMin-1], false},
		{"not HTML", "text/css; charset=utf-8", "", big, false},
		{"already encoded", "text/html; charset=utf-8", "br", big, false},
		{"no body", "", "", "", false},
	} {
		h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// The status first, as the 404 tag page does it.
			w.WriteHeader(http.StatusBadGateway)
			if tt.ctype != "" {
				w.Header().Set("Content-Type", tt.ctype)
			}
			if tt.encoding != "" {
				w.Header().Set("Content-Encoding", tt.encoding)
			}
			if tt.body == "" {
				return
			}
			// In two writes, to check they end up in one gzip stream.
			_, _ = w.Write([]byte(tt.body[:len(tt.body)/2]))
			_, _ = w.Write([]byte(tt.body[len(tt.body)/2:]))
		}), nil)
		log.Reset()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusBadGateway || !strings.Contains(log.String(), "status=502") {
			t.Errorf("%s: status = %d, log %q", tt.name, rec.Code, log)
		}
		body := rec.Body.String()
		if got := rec.Header().Get("Content-Encoding") == "gzip"; got != tt.gzip {
			t.Errorf("%s: gzip = %v, want %v", tt.name, got, tt.gzip)
		} else if got {
			zr, err := gzip.NewReader(rec.Body)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(zr)
			body = string(b)
		}
		if body != tt.body {
			t.Errorf("%s: body differs from what the handler wrote", tt.name)
		}
	}
}
