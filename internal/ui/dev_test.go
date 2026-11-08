//go:build dev

package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevReadsDiskOnEveryCall(t *testing.T) {
	t.Chdir(t.TempDir())
	write := func(name, data string) {
		t.Helper()
		name = filepath.Join("internal/ui", name)
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("static/app.css", "a{}")
	write("templates/page.html", "one")

	tmpl, err := Templates(nil)
	if err != nil {
		t.Fatal(err)
	}
	before := StaticName("app.css")

	write("static/app.css", "b{}")
	write("templates/page.html", "two")

	if after := StaticName("app.css"); after == before {
		t.Errorf("hash didn't change after an edit: %s", after)
	}
	tpl, err := tmpl()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tpl.ExecuteTemplate(&b, "page.html", nil); err != nil || b.String() != "two" {
		t.Errorf("template = %q, %v; want the edited text", b.String(), err)
	}
}
