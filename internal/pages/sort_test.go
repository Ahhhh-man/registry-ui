package pages

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

// rowRepos returns the repositories of the catalog rows in body, in order.
func rowRepos(body string) []string {
	var repos []string
	for _, m := range regexp.MustCompile(`<a href="/([^"]+)" data-row`).FindAllStringSubmatch(body, -1) {
		repos = append(repos, m[1])
	}
	return repos
}

func TestCatalogSorts(t *testing.T) {
	for _, c := range []struct{ sort, want string }{
		{"", "bitnami/kafka bitnami/minio charts/podinfo library/nginx library/postgres library/redis myteam/api myteam/web/frontend"},
		{"nope", "bitnami/kafka bitnami/minio charts/podinfo library/nginx library/postgres library/redis myteam/api myteam/web/frontend"},
		{"name-desc", "myteam/web/frontend myteam/api library/redis library/postgres library/nginx charts/podinfo bitnami/minio bitnami/kafka"},
		// By the latest (or newest) tag's created date; the Helm chart has none, so it goes last.
		{"created", "library/nginx bitnami/minio myteam/api library/postgres bitnami/kafka myteam/web/frontend library/redis charts/podinfo"},
		// Ties (4, 3 and 2 tags) stay A–Z.
		{"tags", "library/nginx library/postgres library/redis myteam/api bitnami/kafka bitnami/minio charts/podinfo myteam/web/frontend"},
	} {
		body := get(t, newTestServer(t, "/"), "/", "/?sort="+c.sort, false).Body.String()
		if got := strings.Join(rowRepos(body), " "); got != c.want {
			t.Errorf("sort %q:\n got %s\nwant %s", c.sort, got, c.want)
		}
		// The detail sorts render their rows loaded, so the tag total adds up.
		if loaded := !strings.Contains(body, "aria-busy"); loaded != (c.sort == "created" || c.sort == "tags") {
			t.Errorf("sort %q: rows loaded = %v", c.sort, loaded)
		}
	}
}

func TestCatalogSortNameMakesNoExtraCalls(t *testing.T) {
	for _, sort := range []string{"", "name-desc"} {
		_, src := getCatalog(t, "/", "/?sort="+sort, false, nil)
		if src.catalog != 1 || src.tags+src.resolve+src.config != 0 {
			t.Errorf("sort %q calls: catalog %d, tags %d, resolve %d, config %d", sort, src.catalog, src.tags, src.resolve, src.config)
		}
	}
}

func TestCatalogSortKeptInLinks(t *testing.T) {
	body := get(t, newTestServer(t, "/"), "/", "/?q=library&sort=tags", false).Body.String()
	contains(t, body,
		`<span class="sr-only">Sort by </span>Most tags<svg`,
		`<a href="/?q=library" class=`, // the default drops sort
		`<a href="/?q=library&amp;sort=name-desc" class=`,
		`<a href="/?q=library&amp;sort=tags" aria-current="true"`,
		`<section id="repo-list" hx-get="/?sort=tags"`,   // as you type
		`<input type="hidden" name="sort" value="tags">`, // the header search, submitted
	)
	if got := strings.Join(rowRepos(body), " "); got != "library/nginx library/postgres library/redis" {
		t.Errorf("filtered and sorted: %s", got)
	}
	if strings.Contains(get(t, newTestServer(t, "/"), "/", "/", false).Body.String(), `name="sort"`) {
		t.Error("the default sort shouldn't add a hidden field")
	}
}

func TestRepoSorts(t *testing.T) {
	s := newTestServer(t, "/")
	sig := cardTags(get(t, s, "/", "/library/nginx?tab=tags", false).Body.String())[2]
	for _, c := range []struct{ sort, want string }{
		{"", "latest stable " + sig + " mainline 1.27.3-alpine 1.27.3 1.27.2-alpine 1.27.2 1.27 1.26.2-alpine 1.26.2 1.26 1.25.5 1.10.3 1.9.15"},
		{"version-asc", "latest 1.9.15 1.10.3 1.25.5 1.26 1.26.2 1.26.2-alpine 1.27 1.27.2 1.27.2-alpine 1.27.3 1.27.3-alpine mainline " + sig + " stable"},
		{"name", "1.10.3 1.25.5 1.26 1.26.2 1.26.2-alpine 1.27 1.27.2 1.27.2-alpine 1.27.3 1.27.3-alpine 1.9.15 latest mainline " + sig + " stable"},
		// Ties keep version order; the signature has no created date, so it goes last.
		{"created", "latest mainline 1.27.3-alpine 1.27.3 1.27 1.27.2-alpine 1.27.2 stable 1.26.2-alpine 1.26.2 1.26 1.25.5 1.10.3 1.9.15 " + sig},
		// By the linux/amd64 size; tags of one image tie and keep version order.
		{"size", "latest mainline 1.27.3 1.27 1.27.2 stable 1.26.2 1.26 1.25.5 1.10.3 1.9.15 1.27.3-alpine 1.27.2-alpine 1.26.2-alpine " + sig},
	} {
		body := get(t, s, "/", "/library/nginx?sort="+c.sort, false).Body.String()
		if got := strings.Join(cardTags(body), " "); got != c.want {
			t.Errorf("sort %q:\n got %s\nwant %s", c.sort, got, c.want)
		}
		// The detail sorts render their cards loaded, Same as included.
		detail := c.sort == "created" || c.sort == "size"
		if loaded := !strings.Contains(body, "aria-busy"); loaded != detail {
			t.Errorf("sort %q: cards loaded = %v", c.sort, loaded)
		}
		if detail && !strings.Contains(body, "Same as") {
			t.Errorf("sort %q: loaded cards lack Same as", c.sort)
		}
	}
}

func TestRepoSortNameMakesNoExtraCalls(t *testing.T) {
	calls := func(sort string) [4]int {
		s := newTestServer(t, "/")
		src := &countingSource{Source: s.Src}
		s.Src = src
		get(t, s, "/", "/library/nginx?sort="+sort, false)
		return [4]int{src.catalog, src.tags, src.resolve, src.config}
	}
	want := calls("")
	for _, sort := range []string{"version-asc", "name"} {
		if got := calls(sort); got != want {
			t.Errorf("sort %q calls (catalog, tags, resolve, config) = %v, the default makes %v", sort, got, want)
		}
	}
}

func TestRepoSortKeptInLinks(t *testing.T) {
	src := &manyTags{Source: newTestServer(t, "/").Src, n: 250}
	s := newTestServer(t, "/")
	s.Src = src

	all, _ := src.Tags(context.Background(), "many")
	slices.Sort(all)
	body := get(t, s, "/", "/many?sort=name&view=list&page=2", false).Body.String()
	if tags := cardTags(body); len(tags) != 100 || tags[0] != all[100] {
		t.Errorf("name sort, page 2 starts at %v, want %v: the whole list is sorted before paging", tags[0], all[100])
	}
	for _, want := range []string{
		`href="/many?page=3&amp;sort=name&amp;tab=tags&amp;view=list"`,                                // Next
		`hx-get="/many?fragment=row&amp;page=2&amp;sort=name&amp;tag=` + all[100] + `&amp;view=list"`, // a row's details
		`<input type="hidden" name="sort" value="name">`,                                              // the filter
		`<a href="/many?sort=size&amp;tab=tags&amp;view=list" class=`,                                 // the menu goes back to page 1
		`<a href="/many?sort=name&amp;tab=tags&amp;view=list" aria-current="true"`,
		`<a href="/many?tab=tags&amp;view=list" class=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}

	rec := get(t, s, "/", "/many?q=v24&sort=version-asc&fragment=tags", true)
	if tags := strings.Join(cardTags(rec.Body.String()), " "); tags != "v24 v240 v241 v242 v243 v244 v245 v246 v247 v248 v249" {
		t.Errorf("filtered, oldest first = %s", tags)
	}
	if u := rec.Header().Get("HX-Replace-Url"); u != "/many?q=v24&sort=version-asc&tab=tags" {
		t.Errorf("HX-Replace-Url = %q", u)
	}

	// A row's fragment finds its tag on the sorted page, not the version-sorted one.
	if rec := get(t, s, "/", "/many?fragment=row&page=2&sort=name&tag="+all[100], true); rec.Code != 200 {
		t.Errorf("row on the name-sorted page 2: status %d", rec.Code)
	}
}

// slowSource takes a while over every manifest.
type slowSource struct{ registry.Source }

func (s slowSource) Resolve(ctx context.Context, repo, ref string) (registry.Manifest, error) {
	time.Sleep(20 * time.Millisecond)
	return s.Source.Resolve(ctx, repo, ref)
}

func TestSortGivesUp(t *testing.T) {
	defer func(d time.Duration) { sortTimeout = d }(sortTimeout)
	sortTimeout = time.Millisecond

	s := newTestServer(t, "/")
	s.Src = slowSource{s.Src}
	body := get(t, s, "/", "/?sort=created", false).Body.String()
	contains(t, body, "took too long to describe every repository, so this list is A–Z instead")
	if repos := rowRepos(body); !slices.IsSorted(repos) {
		t.Errorf("rows %v, want A–Z", repos)
	}

	body = get(t, s, "/", "/library/nginx?sort=size", false).Body.String()
	if !strings.Contains(body, "took too long to describe every tag, so this list is in version order instead") {
		t.Error("no note for a sort that gave up")
	}
	if tags := cardTags(body); tags[0] != "latest" || tags[len(tags)-1] != "1.9.15" {
		t.Errorf("tags %v, want version order", tags)
	}
}

func TestNewestFirst(t *testing.T) {
	now := time.Now()
	dates := []time.Time{{}, now.Add(-time.Hour), now, {}, now.Add(-time.Hour)}
	slices.SortStableFunc(dates, newestFirst)
	if !dates[0].Equal(now) || !dates[1].Equal(now.Add(-time.Hour)) || !dates[2].Equal(dates[1]) || !dates[3].IsZero() || !dates[4].IsZero() {
		t.Errorf("order = %v", dates)
	}
}

// TestUpdatedSorts runs storage mode, where each tag's time is when its link
// last changed.
func TestUpdatedSorts(t *testing.T) {
	r := registrytest.NewStorage(t)
	src, err := registry.NewStorage(r.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, "/")
	s.Src = src

	now := time.Now()
	repos, _ := src.Catalog(t.Context())
	for _, repo := range repos {
		tags, _ := src.Tags(t.Context(), repo)
		for _, tag := range tags {
			r.SetTagTime(t, repo, tag, now.Add(-365*24*time.Hour))
		}
	}
	for _, tt := range []struct {
		repo, tag string
		ago       time.Duration
	}{
		{registrytest.RepoSingle, registrytest.TagSingle, time.Hour},
		{registrytest.RepoPaged, "v3", 2 * time.Hour},
		{registrytest.RepoPaged, "v5", 3 * time.Hour}, // ties with v7
		{registrytest.RepoPaged, "v7", 3 * time.Hour},
		{registrytest.RepoHelm, registrytest.TagHelm, 4 * time.Hour}, // ties with the nested repo
		{registrytest.RepoNested, registrytest.TagNested, 4 * time.Hour},
	} {
		r.SetTagTime(t, tt.repo, tt.tag, now.Add(-tt.ago))
	}

	// A repo goes by its newest tag; ties stay A–Z, and nginx is oldest.
	body := get(t, s, "/", "/?sort=updated", false).Body.String()
	contains(t, body, `<span class="sr-only">Sort by </span>Recently updated<svg`)
	want := strings.Join([]string{registrytest.RepoSingle, registrytest.RepoPaged, registrytest.RepoHelm, registrytest.RepoNested, registrytest.RepoIndex}, " ")
	if got := strings.Join(rowRepos(body), " "); got != want {
		t.Errorf("catalog:\n got %s\nwant %s", got, want)
	}

	// Ties keep version order, as does everything else, a year old.
	body = get(t, s, "/", "/"+registrytest.RepoPaged+"?sort=updated", false).Body.String()
	if got := strings.Join(cardTags(body), " "); got != "v3 v7 v5 v10 v9 v8 v6 v4 v2 v1" {
		t.Errorf("tags: got %s", got)
	}

	card := get(t, s, "/", "/"+registrytest.RepoPaged+"?fragment=card&tag=v3", true).Body.String()
	contains(t, card, "Updated today")
	list := get(t, s, "/", "/"+registrytest.RepoPaged+"?view=list", false).Body.String()
	contains(t, list, ">Updated</th>")
	row := get(t, s, "/", "/"+registrytest.RepoPaged+"?view=list&fragment=row&tag=v7", true).Body.String()
	contains(t, row, `<span title="`+format.Date(now.Add(-3*time.Hour))+`">today</span>`)
	tag := get(t, s, "/", "/"+registrytest.RepoSingle+":"+registrytest.TagSingle, false).Body.String()
	contains(t, tag, "Updated today", "last pulled")
}

// The registry API has no tag times, so nothing about them shows.
func TestNoUpdatedInAPIMode(t *testing.T) {
	api, err := registry.NewAPI(registrytest.New(t).URL, false)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, "/")
	s.Src = api
	for _, path := range []string{
		"/?sort=updated",
		"/" + registrytest.RepoPaged + "?sort=updated",
		"/" + registrytest.RepoPaged + "?view=list",
		"/" + registrytest.RepoPaged + "?view=list&fragment=row&tag=v3",
		"/" + registrytest.RepoPaged + "?fragment=card&tag=v3",
		"/" + registrytest.RepoSingle + ":" + registrytest.TagSingle,
	} {
		body := get(t, s, "/", path, strings.Contains(path, "fragment")).Body.String()
		if strings.Contains(body, "Updated") || strings.Contains(body, "updated") {
			t.Errorf("%s mentions updated:\n%s", path, body)
		}
	}
}
