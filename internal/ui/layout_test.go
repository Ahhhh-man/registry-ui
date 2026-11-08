package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheme(t *testing.T) {
	for cookie, want := range map[string]string{
		"":      "",
		"dark":  "dark",
		"light": "light",
		"Dark":  "",
		"x y":   "",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "theme", Value: cookie})
		}
		if got := Theme(r); got != want {
			t.Errorf("theme=%q: Theme = %q, want %q", cookie, got, want)
		}
	}
}

func TestTileClass(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{"library/nginx", "library/redis", "a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"} {
		c := TileClass(name)
		if c != TileClass(name) {
			t.Fatalf("TileClass(%q) isn't stable", name)
		}
		if len(c) != len("bg-tile-1") || !strings.HasPrefix(c, "bg-tile-") || c[8] < '1' || c[8] > '8' {
			t.Fatalf("TileClass(%q) = %q", name, c)
		}
		seen[c] = true
	}
	if len(seen) < 6 {
		t.Errorf("18 names used only %d of 8 colours: %v", len(seen), seen)
	}
}

func TestIcon(t *testing.T) {
	got, err := Icon("search", `size-5 "><x`)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.HasPrefix(s, `<svg class="icon size-5 &#34;&gt;&lt;x" `) || !strings.Contains(s, `aria-hidden="true"`) || !strings.HasSuffix(s, "</svg>") {
		t.Errorf("Icon = %s", s)
	}
	if _, err := Icon("nope"); err == nil {
		t.Error("unknown icon rendered without error")
	}
}
