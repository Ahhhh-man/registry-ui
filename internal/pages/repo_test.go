package pages

import (
	"context"
	"fmt"
	"html"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// get serves path through the router at base, as htmx when fragment is set.
func get(t *testing.T, s *Server, base, path string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if htmx {
		r.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	web.New(base, s.Handlers()).ServeHTTP(rec, r)
	return rec
}

// cardTags returns the tags of the cards in body, in order.
func cardTags(body string) []string {
	var tags []string
	for _, m := range regexp.MustCompile(`<a href="[^"]*:([^"]+)" title="`).FindAllStringSubmatch(body, -1) {
		tags = append(tags, m[1])
	}
	return tags
}

func TestRepoPage(t *testing.T) {
	s := newTestServer(t, "/")
	rec := get(t, s, "/", "/library/nginx", false)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>library/nginx · registry.example.com</title>",
		`<span class="font-medium text-faint">library/</span>nginx</span>`,
		"Official build of Nginx",
		">15 tags</span>", ">6 platforms</span>", ">Created 2 days ago</span>",
		`aria-current="page">nginx</li>`,
		`data-hotkey="/"`, `hx-vals='{"fragment": "tags"}'`,
		"Version, newest first",
		`value="list" aria-pressed="false" disabled`,
		// Cards wait for their details until they scroll into view.
		`hx-get="/library/nginx?fragment=card&amp;tag=1.27.3" hx-trigger="revealed"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	// latest is pinned; words sort above numbers; 1.10 is newer than 1.9.
	if got := strings.Join(cardTags(body), " "); !strings.HasPrefix(got, "latest stable sha256-") || !strings.HasSuffix(got, "1.25.5 1.10.3 1.9.15") {
		t.Errorf("order = %s", got)
	}
	_, main, _ := strings.Cut(body, "<main") // the layout's own scripts are allowed
	checkMarkup(t, main)
}

func TestRepoFilterAndPages(t *testing.T) {
	src := &manyTags{Source: newTestServer(t, "/").Src, n: 250}
	s := newTestServer(t, "/")
	s.Src = src

	body := get(t, s, "/", "/many?view=list&q=v", false).Body.String()
	tags := cardTags(body)
	if len(tags) != 100 || tags[0] != "v250" || tags[99] != "v151" {
		t.Errorf("page 1 = %d tags, %v … %v", len(tags), tags[0], tags[len(tags)-1])
	}
	if !strings.Contains(body, "Page 1 of 3") || !strings.Contains(body, `href="/many?page=2&amp;q=v&amp;view=list"`) {
		t.Error("page 1 lacks its pagination, with the filter and view kept")
	}

	body = get(t, s, "/", "/many?page=3", false).Body.String()
	if tags := cardTags(body); len(tags) != 50 || tags[49] != "v1" || !strings.Contains(body, `href="/many?page=2"`) || strings.Contains(body, ">Next<") {
		t.Errorf("page 3 = %v", tags)
	}

	rec := get(t, s, "/", "/many?q=V24&fragment=tags", true)
	body = rec.Body.String()
	if tags := cardTags(body); strings.Join(tags, " ") != "v249 v248 v247 v246 v245 v244 v243 v242 v241 v240 v24" {
		t.Errorf("filter V24 = %v", tags)
	}
	if strings.Contains(body, "<!doctype html>") || strings.Contains(body, "Pages") {
		t.Error("the tags fragment should be the bare list, on one page")
	}
	if u := rec.Header().Get("HX-Replace-Url"); u != "/many?q=V24" {
		t.Errorf("HX-Replace-Url = %q", u)
	}

	body = get(t, s, "/", "/many?q=nope&fragment=tags", true).Body.String()
	if !strings.Contains(body, "No tags match “nope”") || !strings.Contains(body, `<a href="/many"`) {
		t.Errorf("no-match state missing:\n%s", body)
	}
	if n := src.tagCalls.Load(); n != 4 {
		t.Errorf("%d tags/list calls for 4 requests", n)
	}
	if n := src.resolves.Load(); n != 2 {
		t.Errorf("%d resolves: only the two full pages' headers should resolve", n)
	}
}

func TestRepoCard(t *testing.T) {
	s := newTestServer(t, "/")
	card := func(repo, tag string) string {
		t.Helper()
		rec := get(t, s, "/", "/"+repo+"?fragment=card&tag="+tag, true)
		if rec.Code != 200 {
			t.Fatalf("%s:%s status %d", repo, tag, rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "<!doctype html>") || strings.Contains(body, "hx-get") {
			t.Errorf("%s:%s should be a loaded card alone", repo, tag)
		}
		checkMarkup(t, body)
		return body
	}

	body := card("library/nginx", "1.27.3")
	for _, want := range []string{
		"Created 2 days ago", "Same as", ">latest</a>, ", ">mainline</a>, ", ">1.27</a>",
		"docker pull registry.example.com/library/nginx:1.27.3",
		"linux/amd64", "linux/arm/v7", "3 more platforms", "group-has-[[open]]/platforms:table-row",
		`href="/library/nginx@sha256:`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("1.27.3 card lacks %s", want)
		}
	}
	if strings.Contains(body, "1.27.3-alpine</a>") || strings.Contains(body, ">stable</a>") {
		t.Error("Same as lists a tag with another digest")
	}
	if body := card("library/nginx", "1.10.3"); strings.Contains(body, "Same as") || strings.Contains(body, "more platform") {
		t.Error("1.10.3 has no aliases and one platform")
	}
	if body := card("library/nginx", "latest"); !strings.Contains(body, ">latest</span>") {
		t.Error("latest card lacks its badge")
	}

	m, _ := s.Src.Resolve(context.Background(), "library/nginx", "1.27.3")
	hex := strings.TrimPrefix(m.Digest, "sha256:")
	body = card("library/nginx", "sha256-"+hex+".sig")
	for _, want := range []string{"sha256-" + hex[:12] + "….sig", "</svg>signature</span>", "cosign verify registry.example.com/library/nginx@" + m.Digest, "Cosign signature"} {
		if !strings.Contains(body, want) {
			t.Errorf("signature card lacks %s", want)
		}
	}

	body = card("charts/podinfo", "6.7.1")
	for _, want := range []string{"</svg>helm chart</span>", "helm pull oci://registry.example.com/charts/podinfo --version 6.7.1", "Helm chart</td>", "14 KB"} {
		if !strings.Contains(body, want) {
			t.Errorf("helm card lacks %s", want)
		}
	}

	if rec := get(t, s, "/", "/library/nginx?fragment=card&tag=nope", true); rec.Code != 404 {
		t.Errorf("unknown tag card: %d", rec.Code)
	}
}

func TestRepoHelmHeader(t *testing.T) {
	body := get(t, newTestServer(t, "/"), "/", "/charts/podinfo", false).Body.String()
	if !strings.Contains(body, `<svg class="icon size-8"`) || !strings.Contains(body, ">2 tags</span>") || strings.Contains(body, "platforms</span>") {
		t.Error("a chart repo shows an anchor tile and no platforms")
	}
}

func TestRepoNotFound(t *testing.T) {
	if rec := get(t, newTestServer(t, "/"), "/", "/no/such", false); rec.Code != 404 {
		t.Errorf("status %d", rec.Code)
	}
}

func TestRepoBasePath(t *testing.T) {
	s := newTestServer(t, "/registry/")
	body := get(t, s, "/registry/", "/registry/library/nginx", false).Body.String()
	for _, want := range []string{`href="/registry/library/nginx:1.27.3"`, `hx-get="/registry/library/nginx?fragment=card&amp;tag=1.27.3"`, `action="/registry/library/nginx"`, `<a href="/registry/" class="hover:text-text">Repositories</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	body = get(t, s, "/registry/", "/registry/library/nginx?fragment=card&tag=1.27.3", true).Body.String()
	if !strings.Contains(body, `href="/registry/library/nginx:latest"`) || !strings.Contains(body, `href="/registry/library/nginx@sha256:`) {
		t.Error("card links lack the base path")
	}
}

// manyTags adds a repo "many" with tags v1…vn to a Source, all one image,
// and counts the calls made for it.
type manyTags struct {
	registry.Source
	n                  int
	tagCalls, resolves atomic.Int32
}

func (m *manyTags) Tags(ctx context.Context, repo string) ([]string, error) {
	if repo != "many" {
		return m.Source.Tags(ctx, repo)
	}
	m.tagCalls.Add(1)
	tags := make([]string, m.n)
	for i := range tags {
		tags[i] = fmt.Sprintf("v%d", i+1)
	}
	return tags, nil
}

func (m *manyTags) Resolve(ctx context.Context, repo, ref string) (registry.Manifest, error) {
	if repo != "many" {
		return m.Source.Resolve(ctx, repo, ref)
	}
	m.resolves.Add(1)
	return m.Source.Resolve(ctx, "library/redis", "7.2.6")
}

func (m *manyTags) Config(ctx context.Context, _, digest string) (registry.ImageConfig, error) {
	return m.Source.Config(ctx, "library/redis", digest)
}

var (
	inlineCode = regexp.MustCompile(`style=|<style|<script|hx-on|javascript:|\son[a-z]+=`)
	classAttr  = regexp.MustCompile(`class="([^"]*)"`)
)

// checkMarkup fails on markup the Content-Security-Policy would block, and
// on classes with no rule in app.css (a typo, or off the design scale).
func checkMarkup(t *testing.T, body string) {
	t.Helper()
	if m := inlineCode.FindString(body); m != "" {
		t.Errorf("markup contains %s", m)
	}
	css, err := os.ReadFile("../ui/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range classAttr.FindAllStringSubmatch(body, -1) {
		for _, c := range strings.Fields(html.UnescapeString(m[1])) {
			esc := regexp.MustCompile(`[^-_a-zA-Z0-9]`).ReplaceAllString(c, `\$0`)
			if !strings.HasPrefix(c, "group") || strings.Contains(c, ":") {
				if !regexp.MustCompile(`\.` + regexp.QuoteMeta(esc) + `[^-_a-zA-Z0-9\\]`).Match(css) {
					t.Errorf("class %q has no CSS", c)
				}
			}
		}
	}
}
