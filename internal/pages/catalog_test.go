package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// countingSource counts the calls made to the Source it wraps.
type countingSource struct {
	registry.Source
	catalog, tags, resolve, config int
	catalogErr                     error
	repos                          []string // replaces the catalog when non-nil
}

func (c *countingSource) Catalog(ctx context.Context) ([]string, error) {
	c.catalog++
	if c.catalogErr != nil || c.repos != nil {
		return c.repos, c.catalogErr
	}
	return c.Source.Catalog(ctx)
}

func (c *countingSource) Tags(ctx context.Context, repo string) ([]string, error) {
	c.tags++
	return c.Source.Tags(ctx, repo)
}

func (c *countingSource) Resolve(ctx context.Context, repo, ref string) (registry.Manifest, error) {
	c.resolve++
	return c.Source.Resolve(ctx, repo, ref)
}

func (c *countingSource) Config(ctx context.Context, repo, digest string) (registry.ImageConfig, error) {
	c.config++
	return c.Source.Config(ctx, repo, digest)
}

// getCatalog serves target from a catalog Server at base, as htmx if hx is set.
func getCatalog(t *testing.T, base, target string, hx bool, setup func(*countingSource)) (*httptest.ResponseRecorder, *countingSource) {
	t.Helper()
	s := newTestServer(t, base)
	src := &countingSource{Source: s.Src}
	if setup != nil {
		setup(src)
	}
	s.Src = src
	r := httptest.NewRequest("GET", target, nil)
	if hx {
		r.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	s.Catalog(rec, r, web.Ref{})
	return rec, src
}

func contains(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q in\n%s", w, body)
		}
	}
}

func TestCatalogPage(t *testing.T) {
	rec, src := getCatalog(t, "/", "/", false, nil)
	body := rec.Body.String()
	contains(t, body,
		"<!doctype html>",
		"<title>Repositories · registry.example.com</title>",
		">registry.example.com</span>",
		`>Repositories <span class="text-[15px] font-normal tracking-normal text-muted">on registry.example.com</span></h1>`,
		`>8</b> <span>repositories</span>`,
		`<span hidden>`, // tags, shown and filled in by app.js
		`data-stat="tags"`,
		"data-repo-list", // so the rows add up to the total
		`<div data-row aria-busy="true" hx-get="/?fragment=row&amp;repo=library/nginx&amp;view=comfortable" hx-trigger="intersect once"`,
		`<a href="/library/nginx" class=`,
		// A nested namespace is one link.
		`<a href="/?q=myteam/web/" aria-label="Repositories in myteam/web/" class=`,
		`>myteam/web/</a><a href="/myteam/web/frontend" class=`,
	)
	if n := strings.Count(body, "data-row"); n != 8 {
		t.Errorf("%d rows, want 8", n)
	}
	// The figures are plain text: no tiles, heading, tagline or chips.
	_, main, _ := strings.Cut(body, "<main")
	for _, gone := range []string{"All repositories", "Every image", " bg-accent-soft", "<h2", `aria-label="Content type" class="flex`, "No description label"} {
		if strings.Contains(main, gone) {
			t.Errorf("the page still has %q", gone)
		}
	}
	if got := pressed(body, "Content type") + "," + pressed(body, "View"); got != "All,Comfortable" {
		t.Errorf("pressed = %s, want All,Comfortable", got)
	}
	if n := strings.Count(segmented(body, "Content type"), "<button "); n != 5 {
		t.Errorf("%d types, want all 5", n)
	}
	checkMarkup(t, main)
	if strings.Index(body, `"/bitnami/kafka"`) > strings.Index(body, `"/myteam/api"`) {
		t.Error("rows aren't A–Z")
	}
	if src.catalog != 1 || src.tags+src.resolve+src.config != 0 {
		t.Errorf("calls: catalog %d, tags %d, resolve %d, config %d; want one catalog call only", src.catalog, src.tags, src.resolve, src.config)
	}
}

func TestCatalogFilter(t *testing.T) {
	rec, _ := getCatalog(t, "/", "/?q=+Kafka", false, nil)
	body := rec.Body.String()
	contains(t, body, `value="Kafka"`, "Matching “Kafka”</h2>", `href="/bitnami/kafka"`, ">8</b> <span>repositories</span>")
	if n := strings.Count(body, "data-row"); n != 1 || strings.Contains(body, "data-repo-list") {
		t.Errorf("%d rows (want 1), or the filtered list is marked for the tag total", n)
	}

	// The as-you-type request is the same URL as htmx.
	rec, _ = getCatalog(t, "/", "/?q=bitnami/", true, nil)
	if n := strings.Count(rec.Body.String(), "data-row"); n != 2 {
		t.Errorf("htmx filter: %d rows, want 2", n)
	}
}

// A search or filter swap says how many repositories matched, for app.js to
// read out from the status region, which sits outside the swapped list.
func TestCatalogAnnounces(t *testing.T) {
	rec, _ := getCatalog(t, "/", "/", false, nil)
	body := rec.Body.String()
	contains(t, body, `data-announce="8 repositories">`, `<div data-live role="status" class="sr-only"></div>`)
	if strings.Index(body, "data-live") < strings.Index(body, "</section>") {
		t.Error("the status region is inside the list htmx swaps")
	}
	for q, want := range map[string]string{
		"bitnami/": `data-announce="2 repositories">`,
		"Kafka":    `data-announce="1 repository">`,
		"zzz":      `data-announce="No repositories match “zzz”">`,
	} {
		rec, _ := getCatalog(t, "/", "/?q="+q, true, nil)
		contains(t, rec.Body.String(), want)
	}
	// A row filling in has nothing to say.
	if rec, _ := getCatalog(t, "/", "/?fragment=row&repo=library/nginx", true, nil); strings.Contains(rec.Body.String(), "data-announce") {
		t.Error("a row fragment announces")
	}
}

// A query ending in "/" is a namespace, matched from the start of the name.
func TestCatalogNamespaceFilter(t *testing.T) {
	repos := []string{"library/nginx", "library/redis/exporter", "mirror/library/nginx", "my-library/app", "team/apps/api"}
	for q, want := range map[string]string{
		"library/":    "[library/nginx library/redis/exporter]",
		"Library/":    "[library/nginx library/redis/exporter]",
		"team/apps/":  "[team/apps/api]",
		"apps/":       "[]", // only in the middle
		"library":     "[library/nginx library/redis/exporter mirror/library/nginx my-library/app]",
		"library/ngi": "[library/nginx mirror/library/nginx]",
	} {
		rec, _ := getCatalog(t, "/", "/?q="+q, false, func(c *countingSource) { c.repos = repos })
		if got := fmt.Sprint(rowRepos(rec.Body.String())); got != want {
			t.Errorf("?q=%s: %s, want %s", q, got, want)
		}
	}
}

// A row's namespace and name are links side by side, never one in the other.
func TestCatalogRowLinks(t *testing.T) {
	for repo, links := range map[string]int{"library/nginx": 2, "gone/away": 2, "solo": 1} {
		rec, _ := getCatalog(t, "/", "/?fragment=row&repo="+repo, true, nil)
		parts := strings.Split(rec.Body.String(), "<a ")
		if len(parts)-1 != links {
			t.Errorf("%s: %d links, want %d", repo, len(parts)-1, links)
		}
		for _, p := range parts[1:] {
			if !strings.Contains(p, "</a>") {
				t.Errorf("%s: a link inside a link: %s", repo, p)
			}
		}
	}
}

func TestCatalogEmpty(t *testing.T) {
	rec, _ := getCatalog(t, "/", "/?q=zzz", false, nil)
	contains(t, rec.Body.String(), "No repositories match “zzz”", "Show all repositories")

	rec, _ = getCatalog(t, "/", "/", false, func(c *countingSource) { c.repos = []string{} })
	body := rec.Body.String()
	contains(t, body, "No repositories yet", ">0</b> <span>repositories</span>", ">0</b> <span>tags</span>")
	if strings.Contains(body, `data-stat="tags"`) {
		t.Error("an empty registry's tag total should be 0, not loading")
	}
}

func TestCatalogRow(t *testing.T) {
	rec, src := getCatalog(t, "/", "/?fragment=row&repo=library/nginx", true, nil)
	body := rec.Body.String()
	if strings.Contains(body, "<!doctype") || strings.Contains(body, "hx-get") || strings.Contains(body, "aria-busy") {
		t.Errorf("want a loaded row alone, got\n%s", body)
	}
	contains(t, body, `<div data-row data-tags="15"`, `<a href="/library/nginx" class=`, "Official build of Nginx", `title="Newest tag: latest">latest</span>`, "15 tags", `<time datetime="`, ` UTC" data-title="Latest created {}">2 days ago</time>`)
	if strings.Contains(body, "platform") || strings.Contains(body, "docker pull") {
		t.Errorf("a comfortable row shows platforms or a pull command:\n%s", body)
	}
	if src.catalog != 0 || src.tags != 1 || src.resolve != 1 || src.config != 1 {
		t.Errorf("calls: catalog %d, tags %d, resolve %d, config %d", src.catalog, src.tags, src.resolve, src.config)
	}

	rec, _ = getCatalog(t, "/", "/?fragment=row&repo=charts/podinfo", true, nil)
	body = rec.Body.String()
	contains(t, body, "2 tags")
	if strings.Contains(body, "No description label") {
		t.Error("a row without a description should say nothing about it")
	}

	rec, _ = getCatalog(t, "/", "/?fragment=row&repo=gone/away", true, nil)
	body = rec.Body.String()
	contains(t, body, `<div data-row`, `<a href="/?q=gone/"`, `<a href="/gone/away" class=`, "Couldn't load details")
	if strings.Contains(body, "data-tags") {
		t.Error("a failed row must not count towards the tag total")
	}
}

// segmented returns the segmented control named label in body: its form,
// whose hidden inputs come before the labelled group of buttons.
func segmented(body, label string) string {
	return regexp.MustCompile(`(?s)<form method="get"[^>]*>(?:<input[^>]*>)*\n<div role="group" aria-label="` + label + `".*?</form>`).FindString(body)
}

// pressed returns the label of the one pressed button of the segmented
// control named label in body.
func pressed(body, label string) string {
	m := regexp.MustCompile(`aria-pressed="true"(?: aria-label="([^"]+)")?[^>]*>(?:<svg.*?</svg>)?([^<]*)<`).FindAllStringSubmatch(segmented(body, label), -1)
	if len(m) != 1 {
		return fmt.Sprintf("%d pressed", len(m))
	}
	return m[0][1] + m[0][2]
}

// tagless empties one repository of its tags.
type tagless struct {
	registry.Source
	repo string
}

func (s tagless) Tags(ctx context.Context, repo string) ([]string, error) {
	if repo == s.repo {
		return nil, nil
	}
	return s.Source.Tags(ctx, repo)
}

// Each view renders a loaded row, a loading row, a failed row and an empty
// repository, and a row costs the same calls whichever view it's in.
func TestCatalogViews(t *testing.T) {
	const pull = "docker pull registry.example.com/library/nginx:latest"
	for view, c := range map[string]struct{ list, loaded, not []string }{
		"compact": {
			[]string{`<div class="divide-y divide-row-border rounded-md border`},
			[]string{"size-5", `title="Newest tag: latest">latest</span>`, "15 tags", "2 days ago</time>"},
			[]string{"Official build of Nginx", pull, "<dl"},
		},
		"comfortable": {
			[]string{`<div class="flex flex-col gap-2">`},
			[]string{"size-9", "Official build of Nginx", `title="Newest tag: latest">latest</span>`, "15 tags", "2 days ago</time>"},
			[]string{pull, "<dl"},
		},
		"detailed": {
			[]string{`<div class="grid grid-cols-[repeat(auto-fill,minmax(min(100%,400px),1fr))] gap-3">`},
			[]string{
				"size-9", "Official build of Nginx", `<dt>Newest tag</dt><dd><a href="/library/nginx:latest" class="relative z-10`, `<dt>Platforms</dt><dd title="linux/amd64, linux/arm64, `,
				`<dt>Tags</dt><dd class="tabular-nums">15</dd>`, "<dt>Created</dt>", "2 days ago</time>", `</span>` + pull + `</code>`, `data-copy="` + pull + `" aria-label="Copy pull command" title="Copy pull command"`,
			},
			[]string{"<dt>Type</dt>"},
		},
	} {
		s := newTestServer(t, "/")
		src := &countingSource{Source: tagless{s.Src, "library/redis"}}
		s.Src = src
		row := func(repo string) string {
			t.Helper()
			body := get(t, s, "/", "/?fragment=row&view="+view+"&repo="+repo, true).Body.String()
			checkMarkup(t, body)
			return body
		}

		// The page: the view's list, its button pressed, and loading rows that ask for the same view.
		body := get(t, s, "/", "/?view="+view, false).Body.String()
		contains(t, body, c.list...)
		contains(t, body, `<div data-row aria-busy="true" hx-get="/?fragment=row&amp;repo=library/nginx&amp;view=`+view+`" hx-trigger="intersect once"`, "animate-pulse")
		if got := pressed(body, "View"); !strings.EqualFold(got, view) {
			t.Errorf("%s: %s is pressed", view, got)
		}
		_, main, _ := strings.Cut(body, "<main")
		checkMarkup(t, main)

		*src = countingSource{Source: src.Source}
		body = row("library/nginx")
		contains(t, body, append(c.loaded, `<div data-row data-tags="15"`, `<a href="/?q=library/"`, `<a href="/library/nginx" class="after:absolute`)...)
		for _, n := range append(c.not, "aria-busy", "hx-get", "animate-pulse") {
			if strings.Contains(body, n) {
				t.Errorf("%s: a loaded row has %q:\n%s", view, n, body)
			}
		}
		if src.catalog != 0 || src.tags != 1 || src.resolve != 1 || src.config != 1 {
			t.Errorf("%s row calls: catalog %d, tags %d, resolve %d, config %d; want one each of tags, resolve and config", view, src.catalog, src.tags, src.resolve, src.config)
		}

		body = row("gone/away")
		contains(t, body, "Couldn't load details", `<a href="/gone/away" class="after:absolute`)
		if strings.Contains(body, "data-tags") || strings.Contains(body, "hx-get") {
			t.Errorf("%s: a failed row counts tags or loads again:\n%s", view, body)
		}

		body = row("library/redis")
		contains(t, body, `data-tags="0"`, "opacity-50", "font-semibold text-muted", ">no tags</span>")
		if strings.Contains(body, "docker pull") {
			t.Errorf("%s: an empty repository has a pull command:\n%s", view, body)
		}
	}

	// Anything but an image says what it is, and a chart pulls with helm.
	body := get(t, newTestServer(t, "/"), "/", "/?fragment=row&view=detailed&repo=charts/podinfo", true).Body.String()
	contains(t, body, "<dt>Type</dt><dd>Helm chart</dd>", "</span>helm pull oci://registry.example.com/charts/podinfo --version ")
	// The description's line stays without one, so a grid row's cards line up.
	contains(t, body, `<p class="h-5 truncate leading-5 text-muted"></p>`)
}

// ?view= picks the view, then the catalog's own cookie; the repository
// page's "view" cookie doesn't.
func TestCatalogViewChoice(t *testing.T) {
	for _, c := range []struct{ target, cookie, want, keep string }{
		{"/", "", "Comfortable", ""},
		{"/?view=nope", "", "Comfortable", ""},
		{"/?view=compact", "", "Compact", "compact"},
		{"/", "catalog-view=detailed", "Detailed", ""},
		{"/?view=compact", "catalog-view=detailed", "Compact", "compact"},
		{"/?view=comfortable", "catalog-view=detailed", "Comfortable", "comfortable"},
		{"/", "catalog-view=nope", "Comfortable", ""},
		{"/", "view=list", "Comfortable", ""},
		{"/", "view=detailed", "Comfortable", ""},
		{"/?fragment=row&repo=library/nginx", "catalog-view=detailed", "", ""},
	} {
		r := httptest.NewRequest("GET", c.target, nil)
		if c.cookie != "" {
			r.Header.Set("Cookie", c.cookie)
		}
		rec := httptest.NewRecorder()
		newTestServer(t, "/").Catalog(rec, r, web.Ref{})
		body := rec.Body.String()
		if c.want == "" { // the row fragment follows the cookie too
			contains(t, body, "<dt>Newest tag</dt>")
			continue
		}
		if got := pressed(body, "View"); got != c.want {
			t.Errorf("%s with cookie %q: %s is pressed, want %s", c.target, c.cookie, got, c.want)
		}
		// Only a view named in the URL is kept by the search, the sort and the type filter.
		if c.keep == "" {
			if strings.Contains(body, `<input type="hidden" name="view"`) || strings.Contains(body, "tags&amp;view=") || strings.Contains(body, `hx-get="/?view=`) {
				t.Errorf("%s with cookie %q: links name a view the URL didn't", c.target, c.cookie)
			}
			continue
		}
		hidden := `<input type="hidden" name="view" value="` + c.keep + `">`
		contains(t, body, hidden+"\n    <input type=\"search\"", `sort=tags&amp;view=`+c.keep+`"`, `<section id="repo-list" hx-get="/?view=`+c.keep+`"`)
		contains(t, segmented(body, "Content type"), hidden)
	}
	// The view switch keeps the search, sort and type.
	body := get(t, newTestServer(t, "/"), "/", "/?q=a&sort=tags&type=image", false).Body.String()
	contains(t, segmented(body, "View"), `data-cookie="catalog-view"`, `<input type="hidden" name="q" value="a">`, `<input type="hidden" name="sort" value="tags">`, `<input type="hidden" name="type" value="image">`,
		`<button type="submit" name="view" value="compact" aria-pressed="false" aria-label="Compact" title="Compact"`)
	// A toolbar click swaps the list in place.
	contains(t, body, `hx-boost="true" hx-target="#repo-list" hx-select="#repo-list"`)
}

func TestCatalogError(t *testing.T) {
	rec, _ := getCatalog(t, "/", "/", false, func(c *countingSource) {
		c.catalogErr = fmt.Errorf("dial: %w", registry.ErrUnavailable)
	})
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", rec.Code)
	}
}

func TestCatalogBasePath(t *testing.T) {
	rec, _ := getCatalog(t, "/registry/", "/registry/", false, nil)
	contains(t, rec.Body.String(),
		`<div data-row aria-busy="true" hx-get="/registry/?fragment=row&amp;repo=library/nginx&amp;view=comfortable"`,
		`<a href="/registry/?q=library/" aria-label="Repositories in library/" class=`,
		`<a href="/registry/library/nginx" class=`,
		`<section id="repo-list" hx-get="/registry/"`,
	)
	rec, _ = getCatalog(t, "/registry/", "/registry/?q=zzz", false, nil)
	contains(t, rec.Body.String(), `<a href="/registry/" class=`)
}

// A sort keeps one summary per repository, which the row fragment reuses
// for the same user until the tag list would have expired.
func TestCatalogRowSummary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestServer(t, "/")
		src := &counted{Source: s.Src}
		s.Src = registry.NewCache(src)
		visit := func(ctx context.Context, target string) int64 {
			t.Helper()
			src.n.Store(0)
			r := httptest.NewRequestWithContext(ctx, "GET", target, nil)
			r.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			s.Catalog(rec, r, web.Ref{})
			if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Couldn't load") {
				t.Fatalf("GET %s: %d\n%s", target, rec.Code, rec.Body)
			}
			return src.n.Load()
		}
		const row = "/?fragment=row&repo=library/nginx"
		anon, alice := t.Context(), registry.WithCredentials(t.Context(), "alice", "secret")

		if n := visit(anon, "/?sort=created"); n == 0 {
			t.Fatal("the sort read nothing")
		}
		if n := visit(anon, row); n != 0 {
			t.Errorf("the row after a sort made %d calls, want 0", n)
		}
		if n := visit(alice, row); n != 3 {
			t.Errorf("another user's row made %d calls, want 3: tags, manifest, config", n)
		}
		time.Sleep(29 * time.Second)
		if n := visit(anon, row); n != 0 {
			t.Errorf("the row before the summary expired made %d calls, want 0", n)
		}
		time.Sleep(time.Second)
		if n := visit(anon, row); n != 3 {
			t.Errorf("the row once the summary expired made %d calls, want 3", n)
		}
	})
}

func TestNewestTag(t *testing.T) {
	for _, c := range []struct {
		tags []string
		want string
	}{
		{[]string{"1.9", "latest", "1.10"}, "latest"},
		{[]string{"1.9", "1.10", "sha256-" + strings.Repeat("a", 64) + ".sig"}, "1.10"},
		{[]string{"sha256-" + strings.Repeat("a", 64) + ".sig"}, ""},
		// Only a full cosign tag is skipped: the repository page agrees.
		{[]string{"1.0.0", "sha256-test"}, "sha256-test"},
		{nil, ""},
	} {
		if got := newestTag(c.tags); got != c.want {
			t.Errorf("newestTag(%q) = %q, want %q", c.tags, got, c.want)
		}
		// The repository page sorts the tags and describes tags[labelTag].
		sorted, page := slices.Clone(c.tags), ""
		format.SortTags(sorted)
		if i := labelTag(sorted); i >= 0 {
			page = sorted[i]
		}
		if got := newestTag(c.tags); got != page {
			t.Errorf("newestTag(%q) = %q, but the repository page describes %q", c.tags, got, page)
		}
	}
}

// TestDiskUsage runs storage mode, which can measure the blob store.
func TestDiskUsage(t *testing.T) {
	src, err := registry.NewStorage(registrytest.NewStorage(t).StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, "/")
	s.Src = src
	usage := func(repo string) string {
		t.Helper()
		n, err := src.DiskUsage(t.Context(), repo)
		if err != nil {
			t.Fatal(err)
		}
		return format.Size(n)
	}

	// The page doesn't wait for the walk: the stat loads after it.
	contains(t, get(t, s, "/", "/", false).Body.String(), `hx-get="/?fragment=disk" hx-trigger="load"`)
	contains(t, get(t, s, "/", "/?fragment=disk", true).Body.String(), ">"+usage("")+"</b> <span>on disk</span>")

	for _, path := range []string{"/" + registrytest.RepoSingle, "/" + registrytest.RepoSingle + "?tab=tags"} {
		contains(t, get(t, s, "/", path, false).Body.String(),
			">"+usage(registrytest.RepoSingle)+" on disk<",
			`title="Layers shared with other repositories count in each of them, so repository totals add up to more than the registry total."`)
	}
}

// The registry API can't tell disk usage, so nothing about it shows.
func TestNoDiskUsageInAPIMode(t *testing.T) {
	api, err := registry.NewAPI(registrytest.New(t).URL, false)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, "/")
	s.Src = api
	for _, path := range []string{"/", "/" + registrytest.RepoSingle, "/" + registrytest.RepoSingle + "?tab=tags"} {
		body := get(t, s, "/", path, false).Body.String()
		if strings.Contains(strings.ToLower(body), "on disk") || strings.Contains(body, "fragment=disk") {
			t.Errorf("%s mentions disk usage:\n%s", path, body)
		}
	}
}
