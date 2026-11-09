package pages

import (
	"context"
	"fmt"
	"html"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
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
	rec := get(t, s, "/", "/library/nginx?tab=tags", false)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>library/nginx · registry.example.com</title>",
		`<span class="font-medium text-faint">library/</span>nginx</span>`,
		"Official build of Nginx",
		">15 tags</span>", ">6 platforms</span>", `<time datetime="`, `UTC">Created 2 days ago</time>`,
		`aria-current="page">nginx</li>`,
		`data-hotkey="/"`, `hx-vals='{"fragment": "tags"}'`,
		"Version, newest first",
		`value="list" aria-pressed="false" class=`, // cards unless asked
		// Cards wait for their details until they scroll into view.
		`hx-get="/library/nginx?fragment=card&amp;tag=1.27.3" hx-trigger="revealed"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if n := strings.Count(body, `data-hotkey="/"`); n != 1 {
		t.Errorf("%d fields take /; only the header search should", n)
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
	if !strings.Contains(body, "Page 1 of 3") || !strings.Contains(body, `href="/many?page=2&amp;q=v&amp;tab=tags&amp;view=list"`) {
		t.Error("page 1 lacks its pagination, with the filter and view kept")
	}

	body = get(t, s, "/", "/many?page=3", false).Body.String()
	if tags := cardTags(body); len(tags) != 50 || tags[49] != "v1" || !strings.Contains(body, `href="/many?page=2&amp;tab=tags"`) || strings.Contains(body, ">Next<") {
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
	if u := rec.Header().Get("HX-Replace-Url"); u != "/many?q=V24&tab=tags" {
		t.Errorf("HX-Replace-Url = %q", u)
	}

	body = get(t, s, "/", "/many?q=nope&fragment=tags", true).Body.String()
	if !strings.Contains(body, "No tags match “nope”") || !strings.Contains(body, `<a href="/many?tab=tags"`) {
		t.Errorf("no-match state missing:\n%s", body)
	}
	if n := src.tagCalls.Load(); n != 4 {
		t.Errorf("%d tags/list calls for 4 requests", n)
	}
	if n := src.resolves.Load(); n != 4 {
		t.Errorf("%d resolves: only each request's header tag should resolve", n)
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
		`<time datetime="`, `UTC">Created 2 days ago</time>`, "Same as", ">latest</a>, ", ">mainline</a>, ", ">1.27</a>",
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

func TestRepoListView(t *testing.T) {
	s := newTestServer(t, "/")
	body := get(t, s, "/", "/library/nginx?view=list", false).Body.String()
	m, _ := s.Src.Resolve(context.Background(), "library/nginx", "1.27.3")
	hex := strings.TrimPrefix(m.Digest, "sha256:")
	for _, want := range []string{
		`value="list" aria-pressed="true"`, `value="cards" aria-pressed="false"`,
		`data-select="/library/nginx"`, `data-selection-bar hidden`, `aria-live="polite"`,
		`data-action="names"`, `data-action="refs"`, `data-action="pulls"`, `data-action="clear"`, `role="status"`,
		`<input type="checkbox" data-select-all aria-label="Select all tags on this page"`,
		`<input type="checkbox" aria-label="Select 1.27.3"`,
		// Rows load like cards, and guess their command until they do.
		`<tr data-tag="1.27.3" data-pull="docker pull registry.example.com/library/nginx:1.27.3" data-ref="registry.example.com/library/nginx:1.27.3" hx-get="/library/nginx?fragment=row&amp;tag=1.27.3&amp;view=list" hx-trigger="revealed"`,
		// A signature's reference is its own tag, not the image it signs.
		`data-pull="cosign verify registry.example.com/library/nginx@` + m.Digest + `" data-ref="registry.example.com/library/nginx:sha256-` + hex + `.sig"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("list view lacks %s", want)
		}
	}
	if strings.Contains(body, "<article") || strings.Contains(body, "Delete") {
		t.Error("the list view shows cards or a delete button")
	}
	if strings.Contains(body, "Click rows") {
		t.Error("the list view shows the selection hint")
	}
	_, main, _ := strings.Cut(body, "<main")
	checkMarkup(t, main)

	if body := get(t, s, "/", "/charts/podinfo?view=list", false).Body.String(); !strings.Contains(body, `data-pull="helm pull oci://registry.example.com/charts/podinfo --version 6.7.1" data-ref="registry.example.com/charts/podinfo:6.7.1"`) {
		t.Error("a chart row should guess helm pull")
	}

	// The cookie picks the view when the URL doesn't.
	view := func(path, cookie string) string {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Cookie", "view="+cookie)
		rec := httptest.NewRecorder()
		web.New("/", s.Handlers()).ServeHTTP(rec, r)
		if strings.Contains(rec.Body.String(), "data-select-all") {
			return "list"
		}
		return "cards"
	}
	if got := view("/library/nginx?tab=tags", "list"); got != "list" {
		t.Errorf("view cookie list gave %s", got)
	}
	if got := view("/library/nginx?view=cards", "list"); got != "cards" {
		t.Errorf("?view=cards with a list cookie gave %s", got)
	}

	rec := get(t, s, "/", "/library/nginx?fragment=row&tag=1.27.3", true)
	body = rec.Body.String()
	for _, want := range []string{
		`<tr data-tag="1.27.3" data-pull="docker pull registry.example.com/library/nginx:1.27.3" data-ref="registry.example.com/library/nginx:1.27.3">`,
		`href="/library/nginx@` + m.Digest + `"`, ">6 platforms</span>", ">69.0 MB</td>", `<time datetime="`, `UTC">2 days ago</time>`,
		`data-copy="docker pull registry.example.com/library/nginx:1.27.3" aria-label="Copy pull command for 1.27.3"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("row lacks %s", want)
		}
	}
	if rec.Code != 200 || strings.Contains(body, "hx-get") {
		t.Errorf("row %d should be loaded:\n%s", rec.Code, body)
	}
	checkMarkup(t, body)
	if body := get(t, s, "/", "/library/nginx?fragment=row&tag=sha256-"+hex+".sig", true).Body.String(); !strings.Contains(body, "Cosign signature</td>") {
		t.Errorf("signature row:\n%s", body)
	}

	// A filter with no matches keeps the bar, so a selection can still be copied.
	body = get(t, s, "/", "/library/nginx?view=list&q=nope&fragment=tags", true).Body.String()
	if !strings.Contains(body, "data-select=") || !strings.Contains(body, "No tags match") {
		t.Errorf("empty list:\n%s", body)
	}
}

func TestRepoOverview(t *testing.T) {
	s := newTestServer(t, "/")
	page := func(path string) string {
		t.Helper()
		rec := get(t, s, "/", path, false)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		body := rec.Body.String()
		_, main, _ := strings.Cut(body, "<main")
		checkMarkup(t, main)
		return body
	}

	body := page("/library/nginx")
	if strings.Index(body, `href="/library/nginx?tab=tags"`) < strings.Index(body, `<a href="/library/nginx" aria-current="page"`) {
		t.Error("Overview isn't the first tab")
	}
	for _, want := range []string{
		`<a href="/library/nginx" aria-current="page"`,
		`<a href="/library/nginx?tab=tags" class=`, // Tags, not current
		"Official build of Nginx",
		`Source</dt><dd class="min-w-0"><a href="https://github.com/nginxinc/docker-nginx" rel="noreferrer"`,
		`Homepage</dt><dd class="min-w-0"><a href="https://nginx.org/" rel="noreferrer"`,
		`Documentation</dt><dd class="min-w-0"><a href="https://nginx.org/en/docs/" rel="noreferrer"`,
		"Read from the OCI labels on latest.",
		`hx-get="/library/nginx" hx-trigger="change" hx-target="#summary"`,
		`<option value="latest" selected>latest</option>`, `<option value="1.9.15">`,
		"<dd>Multi-platform image</dd>", "linux/amd64</span>", "<dt>Platforms</dt><dd>6 platforms</dd>", `<dt>Created</dt><dd><time datetime="`, `UTC">2 days ago</time>`,
		"docker pull registry.example.com/library/nginx:latest",
		"<noscript>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("overview lacks %s", want)
		}
	}
	if strings.Contains(body, "Filter tags") || strings.Contains(body, "hx-trigger=\"revealed\"") {
		t.Error("the overview shows the Tags tab's list")
	}

	body = page("/library/nginx?tab=overview&tag=1.10.3")
	if !strings.Contains(body, `<option value="1.10.3" selected>`) || !strings.Contains(body, "<dd>Image</dd>") || !strings.Contains(body, "nginx:1.10.3") {
		t.Error("?tag= doesn't pick the summary's tag")
	}
	if body := page("/library/nginx?tab=overview&tag=nope"); !strings.Contains(body, `<option value="latest" selected>`) {
		t.Error("an unknown ?tag= should fall back to latest")
	}

	body, _, _ = strings.Cut(page("/myteam/web/frontend?tab=overview"), "<footer") // the footer has its own external links
	if !strings.Contains(body, "No description or links.") || !strings.Contains(body, "v2.0.0 has none it can show") || strings.Contains(body, "rel=\"noreferrer\"") {
		t.Error("a repo without labels should say so")
	}
	if body := page("/charts/podinfo?tab=overview"); !strings.Contains(body, "<dd>Helm chart</dd>") || !strings.Contains(body, "helm pull oci://registry.example.com/charts/podinfo --version 6.7.1") {
		t.Error("a chart's summary lacks its type or command")
	}

	// The dropdown swaps in the summary alone and keeps the URL shareable.
	m, _ := s.Src.Resolve(context.Background(), "library/nginx", "1.27.3")
	sig := "sha256-" + strings.TrimPrefix(m.Digest, "sha256:") + ".sig"
	rec := get(t, s, "/", "/library/nginx?tab=overview&fragment=summary&tag="+sig, true)
	body = rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, "<!doctype html>") || strings.Contains(body, "<option") || !strings.Contains(body, "<dd>Cosign signature</dd>") || !strings.Contains(body, "cosign verify ") {
		t.Errorf("signature summary:\n%s", body)
	}
	checkMarkup(t, body)
	if u := rec.Header().Get("HX-Replace-Url"); u != "/library/nginx?tab=overview&tag="+sig {
		t.Errorf("HX-Replace-Url = %q", u)
	}
	if rec := get(t, s, "/", "/library/nginx?tab=overview&fragment=summary&tag=nope", true); rec.Code != 404 {
		t.Errorf("unknown tag summary: %d", rec.Code)
	}
}

// labelled is a Source whose image configs all carry the given labels.
type labelled struct {
	registry.Source
	labels map[string]string
}

func (l labelled) Config(ctx context.Context, repo, digest string) (registry.ImageConfig, error) {
	cfg, err := l.Source.Config(ctx, repo, digest)
	cfg.Labels = l.labels
	return cfg, err
}

func TestRepoOverviewLinks(t *testing.T) {
	const src, home, docs = "org.opencontainers.image.source", "org.opencontainers.image.url", "org.opencontainers.image.documentation"
	for name, c := range map[string]struct {
		labels map[string]string
		links  []string // in order; nil for the nothing-to-show text
	}{
		"all three": {
			map[string]string{docs: "https://docs.example.com", home: "http://example.com", src: "https://git.example.com/a"},
			[]string{`Source</dt><dd class="min-w-0"><a href="https://git.example.com/a"`, `Homepage</dt><dd class="min-w-0"><a href="http://example.com"`, `Documentation</dt><dd class="min-w-0"><a href="https://docs.example.com"`},
		},
		"url is the source": {
			map[string]string{src: "https://git.example.com/a", home: "https://git.example.com/a"},
			[]string{`Source</dt><dd class="min-w-0"><a href="https://git.example.com/a"`},
		},
		"url is the source but for a slash": {
			map[string]string{src: "https://git.example.com/a/", home: "https://git.example.com/a"},
			[]string{`Source</dt><dd class="min-w-0"><a href="https://git.example.com/a/"`},
		},
		"not web links": {
			map[string]string{src: "javascript:alert(1)", home: "example.com", docs: "https://docs.example.com"},
			[]string{`Documentation</dt><dd class="min-w-0"><a href="https://docs.example.com"`},
		},
		"only documentation": {
			map[string]string{docs: "https://docs.example.com"},
			[]string{`Documentation</dt><dd class="min-w-0"><a href="https://docs.example.com"`},
		},
		"nothing usable": {map[string]string{src: "javascript:alert(1)", home: "example.com"}, nil},
		"none":           {nil, nil},
	} {
		s := newTestServer(t, "/")
		s.Src = labelled{s.Src, c.labels}
		body, _, _ := strings.Cut(get(t, s, "/", "/myteam/api", false).Body.String(), "<footer") // the footer has its own external links
		if got := strings.Count(body, `rel="noreferrer"`); got != len(c.links) {
			t.Errorf("%s: %d links, want %d", name, got, len(c.links))
		}
		at := 0
		for _, want := range c.links {
			i := strings.Index(body[at:], want)
			if i < 0 {
				t.Errorf("%s: lacks %s, or it's out of order", name, want)
				break
			}
			at += i
		}
		if (c.links == nil) != strings.Contains(body, "No description or links.") {
			t.Errorf("%s: wrong About text", name)
		}
		if strings.Contains(body, "javascript:") || strings.Contains(body, `href="example.com"`) {
			t.Errorf("%s: a label that isn't a web link is shown", name)
		}
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
	body := get(t, s, "/registry/", "/registry/library/nginx?tab=tags", false).Body.String()
	for _, want := range []string{`href="/registry/library/nginx:1.27.3"`, `hx-get="/registry/library/nginx?fragment=card&amp;tag=1.27.3"`, `action="/registry/library/nginx"`, `<a href="/registry/" class="hover:text-text">Repositories</a>`, `<a href="/registry/?q=library/" class="hover:text-text">library</a>`} {
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

// noAmd64 is a registry that lacks every index's linux/amd64 manifest.
type noAmd64 struct{ registry.Source }

func (s noAmd64) Resolve(ctx context.Context, repo, ref string) (registry.Manifest, error) {
	m, err := s.Source.Resolve(ctx, repo, ref)
	for i, p := range m.Platforms {
		if m.Kind == registry.KindIndex && p.OS == "linux" && p.Arch == "amd64" {
			m.Platforms[i] = registry.Platform{OS: p.OS, Arch: p.Arch, Digest: p.Digest, MediaType: p.MediaType, Missing: true}
		}
	}
	return m, err
}

// A missing platform has no size or config, so the sorts go by the next
// platform instead: arm64, which in the fake is the same age and a fixed
// share of the size, so the order doesn't change.
func TestSortsSkipMissingPlatforms(t *testing.T) {
	for _, sort := range []string{"created", "size"} {
		s := newTestServer(t, "/")
		want := cardTags(get(t, s, "/", "/library/nginx?sort="+sort, false).Body.String())
		s.Src = noAmd64{s.Src}
		if got := cardTags(get(t, s, "/", "/library/nginx?sort="+sort, false).Body.String()); !slices.Equal(got, want) {
			t.Errorf("sort %q without amd64:\n got %v\nwant %v", sort, got, want)
		}
	}
}

// In the fake, redis:7.2.6 lacks linux/amd64 and linux/s390x, and 7.0.15
// lacks both of its platforms.
func TestRepoMissingPlatforms(t *testing.T) {
	s := newTestServer(t, "/")
	m := resolve(t, "library/redis", "7.2.6")
	arm64, amd64 := m.Platforms[1], m.Platforms[0]

	// The size sort goes by arm64 for 7.2.6; 7.0.15 has no size, so it's last.
	if got := strings.Join(cardTags(get(t, s, "/", "/library/redis?sort=size", false).Body.String()), " "); got != "latest 7.4.1 7.2.6 7.4.1-alpine 7.0.15" {
		t.Errorf("size sort = %s", got)
	}

	body := get(t, s, "/", "/library/redis?fragment=row&tag=7.2.6", true).Body.String()
	contains(t, body, "linux/amd64 (missing),", ">4 platforms (2 missing)</span>", `title="linux/arm64">`+format.Size(arm64.Size)+"</td>", "2 months ago")
	body = get(t, s, "/", "/library/redis?fragment=row&tag=7.0.15", true).Body.String()
	contains(t, body, ">2 platforms, all missing</span>", ">Unknown</td>")

	// Cards list the missing platforms last, dimmed and without a link.
	body = get(t, s, "/", "/library/redis?fragment=card&tag=7.2.6", true).Body.String()
	if strings.Index(body, ">linux/amd64</span>") < strings.Index(body, ">linux/ppc64le</span>") || strings.Count(body, ">Missing</span>") != 2 || strings.Contains(body, amd64.Digest) || !strings.Contains(body, ">1 more platform (2 missing)</span>") {
		t.Errorf("card for 7.2.6:\n%s", body)
	}
	body = get(t, s, "/", "/library/redis?fragment=card&tag=7.0.15", true).Body.String()
	if strings.Count(body, ">Missing</span>") != 2 || strings.Contains(body, "OCI artifact") {
		t.Errorf("card for 7.0.15:\n%s", body)
	}

	body = get(t, s, "/", "/library/redis?tab=overview&tag=7.2.6", false).Body.String()
	contains(t, body, "<dd>Multi-platform image</dd>", "<dt>Platforms</dt><dd>4 platforms (2 missing)</dd>", "linux/arm64</span>")
	body = get(t, s, "/", "/library/redis?tab=overview&tag=7.0.15", false).Body.String()
	contains(t, body, "<dd>Multi-platform image</dd>", "<dd><span>Unknown</span></dd>", "<dt>Platforms</dt><dd>2 platforms, all missing</dd>")
}

// Against a real registry, in both modes, a repository with no tags left
// shows as empty wherever it shows, rather than as an error, whether
// tags/list answers an empty list or a 404.
func TestEmptyReposInRegistry(t *testing.T) {
	api, err := registry.NewAPI(registrytest.New(t).URL, false)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := registry.NewStorage(registrytest.NewStorage(t).StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	empty := []string{registrytest.RepoEmpty, registrytest.RepoUntagged}
	for name, src := range map[string]registry.Source{"api": api, "storage": storage} {
		s := newTestServer(t, "/")
		s.Src, s.Storage = src, name == "storage"
		check := func(what, body string, want, not []string) {
			t.Helper()
			for _, w := range want {
				if !strings.Contains(body, w) {
					t.Errorf("%s: %s lacks %q:\n%s", name, what, w, body)
				}
			}
			for _, n := range not {
				if strings.Contains(body, n) {
					t.Errorf("%s: %s has %q:\n%s", name, what, n, body)
				}
			}
		}

		for _, repo := range empty {
			// The catalog row is dimmed, says so and counts no tags.
			row := get(t, s, "/", "/?fragment=row&repo="+repo, true).Body.String()
			check("catalog row", row, []string{`data-tags="0"`, "font-semibold text-muted", ">no tags</span>", ">0 tags</span>"}, []string{"Couldn't load", "No description label"})

			// Both tabs explain, without an error.
			for _, target := range []string{"", "?tab=tags", "?tab=tags&fragment=tags"} {
				rec := get(t, s, "/", "/"+repo+target, strings.Contains(target, "fragment"))
				if rec.Code != 200 {
					t.Errorf("%s: /%s%s: status %d", name, repo, target, rec.Code)
				}
				body := rec.Body.String()
				check(repo+target, body, []string{"No tags", "Every tag in this repository has been deleted.", "docker/registry/v2/repositories/" + repo + " by hand"}, []string{"Tag summary", "Filter tags"})
				if ro := "This UI only reads storage"; s.Storage != strings.Contains(body, ro) {
					t.Errorf("%s: /%s%s: says %q: %t", name, repo, target, ro, !s.Storage)
				}
			}
		}

		// They stay in the list, drop out under a type filter, and come last by tag count.
		all := rowRepos(get(t, s, "/", "/", false).Body.String())
		for _, repo := range empty {
			if !slices.Contains(all, repo) {
				t.Errorf("%s: catalog lacks %s: %v", name, repo, all)
			}
		}
		if got := rowRepos(get(t, s, "/", "/?type=image", false).Body.String()); slices.ContainsFunc(got, func(r string) bool { return slices.Contains(empty, r) }) {
			t.Errorf("%s: ?type=image lists an empty repo: %v", name, got)
		}
		if got := rowRepos(get(t, s, "/", "/?sort=tags", false).Body.String()); len(got) < 2 || !slices.Equal(got[len(got)-2:], empty) {
			t.Errorf("%s: ?sort=tags should put %v last: %v", name, empty, got)
		}
	}

	// A 404 from tags/list can't tell an unknown name from an untagged
	// repository, so the API shows that as empty too. Storage can tell.
	s := newTestServer(t, "/")
	s.Src = api
	if rec := get(t, s, "/", "/nope/missing", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Every tag in this repository has been deleted.") {
		t.Errorf("api: unknown repo: status %d", rec.Code)
	}
	s.Src = storage
	if rec := get(t, s, "/", "/nope/missing", false); rec.Code != 404 {
		t.Errorf("storage: unknown repo: status %d", rec.Code)
	}
}
