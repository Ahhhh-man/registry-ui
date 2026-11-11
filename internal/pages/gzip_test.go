package pages

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// newGzipUI returns the whole UI as main wires it, over the fake registry,
// with the request log switched off for the test.
func newGzipUI(t testing.TB) (*Server, http.Handler) {
	t.Helper()
	old := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { slog.SetDefault(old) })
	s := newTestServer(t, "/")
	h := s.Handlers()
	h.Static = ui.StaticHandler()
	return s, web.Middleware(web.New("/", h), ui.ErrorRenderer(s.Templates, s.Header))
}

// serve answers target, asked for with the Accept-Encoding accept, and as
// htmx when htmx is set.
func serve(h http.Handler, method, target, accept string, htmx bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if accept != "" {
		r.Header.Set("Accept-Encoding", accept)
	}
	if htmx {
		r.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestGzipPagesAndFragments(t *testing.T) {
	_, h := newGzipUI(t)
	for _, tt := range []struct {
		name, target string
		htmx         bool
		status       int
	}{
		{"page", "/", false, http.StatusOK},
		{"fragment", "/library/nginx?tab=tags&fragment=tags", true, http.StatusOK},
		{"error page", "/no/such-repo", false, http.StatusNotFound},
		{"404 tag page", "/library/nginx:no-such-tag", false, http.StatusNotFound},
	} {
		plain := serve(h, http.MethodGet, tt.target, "", tt.htmx)
		if plain.Code != tt.status || plain.Header().Get("Content-Encoding") != "" || plain.Body.Len() < 1024 {
			t.Fatalf("%s: plain = %d, encoding %q, %d bytes", tt.name, plain.Code, plain.Header().Get("Content-Encoding"), plain.Body.Len())
		}
		if refused := serve(h, http.MethodGet, tt.target, "gzip;q=0", tt.htmx); refused.Header().Get("Content-Encoding") != "" || refused.Body.String() != plain.Body.String() {
			t.Errorf("%s: gzip;q=0 wasn't answered plain", tt.name)
		}

		rec := serve(h, http.MethodGet, tt.target, "gzip, deflate, br", tt.htmx)
		if rec.Code != tt.status {
			t.Errorf("%s: gzipped status = %d, want %d", tt.name, rec.Code, tt.status)
		}
		hd := rec.Header()
		if hd.Get("Content-Encoding") != "gzip" || hd.Get("Content-Type") != "text/html; charset=utf-8" || hd.Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
			t.Errorf("%s: gzipped headers = %v, body %d bytes", tt.name, hd, rec.Body.Len())
		}
		if rec.Body.Len() >= plain.Body.Len() {
			t.Errorf("%s: gzipped is %d bytes, plain %d", tt.name, rec.Body.Len(), plain.Body.Len())
		}
		if gunzip(t, rec.Body.Bytes()) != plain.Body.String() {
			t.Errorf("%s: the decompressed body differs from the plain one", tt.name)
		}
		for _, rec := range []*httptest.ResponseRecorder{plain, rec} {
			if vary := strings.Join(rec.Header().Values("Vary"), ","); !strings.Contains(vary, "Accept-Encoding") {
				t.Errorf("%s: Vary = %q", tt.name, vary)
			}
		}
		if vary := strings.Join(hd.Values("Vary"), ","); !strings.Contains(vary, "HX-Request") {
			t.Errorf("%s: Vary = %q, lost HX-Request", tt.name, vary)
		}

		// HEAD has no body to compress.
		if head := serve(h, http.MethodHead, tt.target, "gzip", tt.htmx); head.Code != tt.status || head.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: HEAD = %d, encoding %q", tt.name, head.Code, head.Header().Get("Content-Encoding"))
		}
	}
}

// Static files are compressed at start-up, so the middleware leaves them be.
func TestGzipLeavesStaticFilesAlone(t *testing.T) {
	_, h := newGzipUI(t)
	target := "/-/static/" + ui.StaticName("app.css")
	for _, accept := range []string{"", "gzip"} {
		direct := serve(http.StripPrefix("/-/static", ui.StaticHandler()), http.MethodGet, target, accept, false)
		rec := serve(h, http.MethodGet, target, accept, false)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), direct.Body.Bytes()) {
			t.Errorf("accept %q: got %d, body differs from the static handler's", accept, rec.Code)
		}
		for _, k := range []string{"Content-Type", "Content-Encoding", "Content-Length", "Vary", "Cache-Control"} {
			if got, want := rec.Header().Values(k), direct.Header().Values(k); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("accept %q: %s = %q, want %q", accept, k, got, want)
			}
		}
	}
}

// BenchmarkCatalog renders a 2,000 repository catalog through the whole
// handler chain, and reports the size of the response.
func BenchmarkCatalog(b *testing.B) {
	s, h := newGzipUI(b)
	repos := make([]string, 2000)
	for i := range repos {
		repos[i] = fmt.Sprintf("team-%02d/service-%04d", i%40, i)
	}
	s.Src = &countingSource{Source: s.Src, repos: repos}
	for _, accept := range []string{"", "gzip"} {
		b.Run("accept="+accept, func(b *testing.B) {
			var size int
			for b.Loop() {
				size = serve(h, http.MethodGet, "/", accept, false).Body.Len()
			}
			b.ReportMetric(float64(size), "bytes/response")
		})
	}
}
