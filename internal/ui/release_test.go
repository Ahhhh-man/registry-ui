//go:build !dev

package ui

import (
	"embed"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A release build's files must be the embedded ones; this fails to compile
// if they ever come from the disk.
var _ embed.FS = files

func TestReleaseServesFromBinary(t *testing.T) {
	t.Chdir(t.TempDir()) // nothing on disk to fall back to
	name := StaticName("htmx.min.js")
	r := httptest.NewRequest(http.MethodGet, "/"+name, nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	StaticHandler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("GET %s = %d %v", name, w.Code, w.Header())
	}
}

func TestTemplatesUseHashedNames(t *testing.T) {
	funcs := map[string]any{"staticURL": func(name string) string { return "/-/static/" + StaticName(name) }}
	tmpl, err := Templates(funcs)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := tmpl()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tpl.ExecuteTemplate(&b, "htmx", nil); err != nil {
		t.Fatal(err)
	}
	if want := `src="/-/static/` + StaticName("htmx.min.js") + `"`; !strings.Contains(b.String(), want) {
		t.Errorf("htmx partial = %q, want it to contain %s", b.String(), want)
	}

	// A typo in a static file name fails the render instead of linking nowhere.
	bad, err := template.New("bad").Funcs(funcs).Parse(`{{staticURL "nope.css"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Execute(io.Discard, nil); err == nil {
		t.Error("unknown static name rendered without error")
	}
}
