//go:build !dev

package ui

import (
	"embed"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
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

var testFuncs = serverFuncs("/", StaticName)

func TestTemplatesUseHashedNames(t *testing.T) {
	tmpl, err := Templates(testFuncs)
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
	b.Reset()
	if err := tpl.ExecuteTemplate(&b, "layout", Page{}); err != nil {
		t.Fatal(err)
	}
	if want := `<link rel="icon" href="/-/static/` + StaticName("favicon.svg") + `"`; !strings.Contains(b.String(), want) {
		t.Errorf("layout lacks %s", want)
	}

	// A typo in a static file name fails the render instead of linking nowhere.
	bad, err := template.New("bad").Funcs(testFuncs).Parse(`{{staticURL "nope.css"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Execute(io.Discard, nil); err == nil {
		t.Error("unknown static name rendered without error")
	}
}

func TestLayout(t *testing.T) {
	tmpl, err := Templates(testFuncs)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := tmpl()
	if err != nil {
		t.Fatal(err)
	}
	for theme, html := range map[string]string{"": `<html lang="en">`, "dark": `<html lang="en" class="dark">`, "light": `<html lang="en" class="light">`} {
		var b strings.Builder
		page := Page{Title: "registry.example.com", PageTitle: "nginx", Theme: theme, Query: `ngi"nx`, Body: "<p>placeholder</p>"}
		if err := tpl.ExecuteTemplate(&b, "layout", page); err != nil {
			t.Fatal(err)
		}
		s := b.String()
		for _, want := range []string{
			"<!doctype html>\n" + html,
			"<title>nginx · registry.example.com</title>",
			`href="/-/static/` + StaticName("app.css") + `"`,
			`src="/-/static/` + StaticName("app.js") + `"`,
			`value="ngi&#34;nx"`,
			"<p>placeholder</p>",
			`<span class="min-w-0 truncate font-semibold">registry.example.com</span>`,
			"data-theme-toggle",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("theme %q: layout lacks %s", theme, want)
			}
		}
		// The CSP forbids inline styles and scripts.
		if strings.Contains(s, "style=") || strings.Contains(s, "<style") || strings.Count(s, "<script") != strings.Count(s, "<script src=") {
			t.Errorf("layout has inline style or script:\n%s", s)
		}
	}
}

func TestLayoutFooter(t *testing.T) {
	tmpl, err := Templates(testFuncs)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := tmpl()
	if err != nil {
		t.Fatal(err)
	}
	const release = `<a href="https://github.com/Ahhhh-man/registry-ui/releases/tag/v0.7.0" rel="noreferrer"`
	for version, want := range map[string]string{"0.7.0": release, "dev": "registry-ui dev</p>", "": "registry-ui dev</p>"} {
		var b strings.Builder
		if err := tpl.ExecuteTemplate(&b, "layout", Page{Version: version}); err != nil {
			t.Fatal(err)
		}
		_, footer, _ := strings.Cut(b.String(), "<footer")
		for _, want := range []string{
			want,
			`<a href="https://github.com/Ahhhh-man/registry-ui" rel="noreferrer"`,
			`<a href="https://github.com/Ahhhh-man/registry-ui#readme" rel="noreferrer"`,
			`<a href="https://github.com/Ahhhh-man/registry-ui/issues/new" rel="noreferrer"`,
		} {
			if !strings.Contains(footer, want) {
				t.Errorf("version %q: footer lacks %s:\n%s", version, want, footer)
			}
		}
		if version != "0.7.0" && strings.Contains(footer, "/releases/") {
			t.Errorf("version %q: footer links to a release:\n%s", version, footer)
		}
		// Links only: nothing in the footer loads from another origin.
		if strings.Contains(footer, "src=") || strings.Contains(footer, "<link") {
			t.Errorf("version %q: footer loads something:\n%s", version, footer)
		}
	}
}

// The fonts are named by hash in tailwind.css; a stale name would 404.
func TestCSSURLsResolve(t *testing.T) {
	css, err := fs.ReadFile(files, "static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	urls := regexp.MustCompile(`url\("?([^")]+)"?\)`).FindAllSubmatch(css, -1)
	if len(urls) < 2 {
		t.Fatalf("found %d url()s in app.css, want the two fonts", len(urls))
	}
	for _, u := range urls {
		r := httptest.NewRequest(http.MethodGet, "/"+string(u[1]), nil)
		w := httptest.NewRecorder()
		StaticHandler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("app.css links %s, which is a %d", u[1], w.Code)
		}
	}
	// Tailwind's default palette is oklch(); none of it may survive.
	if strings.Contains(string(css), "oklch(") {
		t.Error("app.css contains Tailwind's default colours")
	}
}
