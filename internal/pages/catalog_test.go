package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		">8</div>",     // Repositories
		`<div hidden>`, // Tags, shown and filled in by app.js
		`data-stat="tags"`,
		"All repositories", // unfiltered
		"data-repo-list",   // so the rows add up to the total
		`<div data-row aria-busy="true" hx-get="/?fragment=row&amp;repo=library/nginx" hx-trigger="intersect once"`,
		`<a href="/library/nginx" class=`,
		// A nested namespace is one link.
		`<a href="/?q=myteam/web/" aria-label="Repositories in myteam/web/" class=`,
		`>myteam/web/</a><a href="/myteam/web/frontend" class=`,
	)
	if n := strings.Count(body, "data-row"); n != 8 {
		t.Errorf("%d rows, want 8", n)
	}
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
	contains(t, body, `value="Kafka"`, "Matching “Kafka”", `href="/bitnami/kafka"`, ">8</div>")
	if n := strings.Count(body, "data-row"); n != 1 || strings.Contains(body, "data-repo-list") {
		t.Errorf("%d rows (want 1), or the filtered list is marked for the tag total", n)
	}

	// The as-you-type request is the same URL as htmx.
	rec, _ = getCatalog(t, "/", "/?q=bitnami/", true, nil)
	if n := strings.Count(rec.Body.String(), "data-row"); n != 2 {
		t.Errorf("htmx filter: %d rows, want 2", n)
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
	contains(t, body, "No repositories yet", ">0</div>")
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
	contains(t, body, `<div data-row data-tags="15"`, `<a href="/library/nginx" class=`, "Official build of Nginx", "6 platforms", "15 tags", `<time datetime="`, ` UTC" data-title="Latest created {}">2 days ago</time>`)
	if src.catalog != 0 || src.tags != 1 || src.resolve != 1 || src.config != 1 {
		t.Errorf("calls: catalog %d, tags %d, resolve %d, config %d", src.catalog, src.tags, src.resolve, src.config)
	}

	rec, _ = getCatalog(t, "/", "/?fragment=row&repo=charts/podinfo", true, nil)
	contains(t, rec.Body.String(), "Helm chart", "2 tags", "No description label")

	rec, _ = getCatalog(t, "/", "/?fragment=row&repo=myteam/web/frontend", true, nil)
	contains(t, rec.Body.String(), "1 platform<", "2 tags")

	rec, _ = getCatalog(t, "/", "/?fragment=row&repo=gone/away", true, nil)
	body = rec.Body.String()
	contains(t, body, `<div data-row`, `<a href="/?q=gone/"`, `<a href="/gone/away" class=`, "Couldn't load details")
	if strings.Contains(body, "data-tags") {
		t.Error("a failed row must not count towards the tag total")
	}
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
		`<div data-row aria-busy="true" hx-get="/registry/?fragment=row&amp;repo=library/nginx"`,
		`<a href="/registry/?q=library/" aria-label="Repositories in library/" class=`,
		`<a href="/registry/library/nginx" class=`,
		`<section id="repo-list" hx-get="/registry/"`,
	)
	rec, _ = getCatalog(t, "/registry/", "/registry/?q=zzz", false, nil)
	contains(t, rec.Body.String(), `<a href="/registry/" class=`)
}

func TestNewestTag(t *testing.T) {
	for _, c := range []struct {
		tags []string
		want string
	}{
		{[]string{"1.9", "latest", "1.10"}, "latest"},
		{[]string{"1.9", "1.10", "sha256-" + strings.Repeat("a", 64) + ".sig"}, "1.10"},
		{[]string{"sha256-" + strings.Repeat("a", 64) + ".sig"}, ""},
		{nil, ""},
	} {
		if got := newestTag(c.tags); got != c.want {
			t.Errorf("newestTag(%q) = %q, want %q", c.tags, got, c.want)
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
	contains(t, get(t, s, "/", "/?fragment=disk", true).Body.String(), ">"+usage("")+"</div>", ">On disk</div>")

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
