package pages

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
)

// chipText sums up the type chips in body as "All 15 · *Images 14 · …",
// the current one starred and each followed by its count, if shown.
func chipText(body string) string {
	nav := regexp.MustCompile(`(?s)<nav aria-label="Content type".*?</nav>`).FindString(body)
	var chips []string
	for _, m := range regexp.MustCompile(`(?s)<a href="[^"]*"( aria-current="true")?[^>]*>(.*?)</a>`).FindAllStringSubmatch(nav, -1) {
		text := strings.Join(strings.Fields(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(m[2], " ")), " ")
		if m[1] != "" {
			text = "*" + text
		}
		chips = append(chips, text)
	}
	return strings.Join(chips, " · ")
}

// retyped makes some of a Source's manifests other artifacts, by repo or
// "repo:tag", and adds a repo, "signatures/nginx", of nginx's signature
// alone, as cosign writes when told to keep signatures apart.
type retyped struct {
	registry.Source
	artifacts []string
}

const sigRepo = "signatures/nginx"

func (s retyped) Catalog(ctx context.Context) ([]string, error) {
	repos, err := s.Source.Catalog(ctx)
	return append(repos, sigRepo), err
}

func (s retyped) Tags(ctx context.Context, repo string) ([]string, error) {
	if repo != sigRepo {
		return s.Source.Tags(ctx, repo)
	}
	tags, err := s.Source.Tags(ctx, "library/nginx")
	return slices.DeleteFunc(tags, func(t string) bool { return !sigTag.MatchString(t) }), err
}

func (s retyped) Resolve(ctx context.Context, repo, ref string) (registry.Manifest, error) {
	if repo == sigRepo {
		repo = "library/nginx"
	}
	m, err := s.Source.Resolve(ctx, repo, ref)
	if slices.Contains(s.artifacts, repo) || slices.Contains(s.artifacts, repo+":"+ref) {
		m.Kind, m.Platforms = registry.KindArtifact, nil
	}
	return m, err
}

func TestCatalogTypes(t *testing.T) {
	images := "bitnami/kafka bitnami/minio library/nginx library/postgres library/redis myteam/api myteam/web/frontend"
	for _, c := range []struct{ target, rows, chips, empty string }{
		// Unfiltered, only the total is known without calls.
		{"/", "bitnami/kafka bitnami/minio charts/podinfo library/nginx library/postgres library/redis myteam/api myteam/web/frontend", "*All 8 · Images · Helm charts · Signatures · Other artifacts", ""},
		{"/?type=nope", "bitnami/kafka bitnami/minio charts/podinfo library/nginx library/postgres library/redis myteam/api myteam/web/frontend", "*All 8 · Images · Helm charts · Signatures · Other artifacts", ""},
		// Filtered, every row is described, so the chips count and hide.
		{"/?type=image", images, "All 8 · *Images 7 · Helm charts 1", ""},
		{"/?type=helm", "charts/podinfo", "All 8 · Images 7 · *Helm charts 1", ""},
		{"/?type=signature", "", "All 8 · Images 7 · Helm charts 1 · *Signatures 0", "No signatures here"},
		{"/?type=artifact", "", "All 8 · Images 7 · Helm charts 1 · *Other artifacts 0", "No artifacts here"},
		// With the name filter.
		{"/?q=bitnami&type=image", "bitnami/kafka bitnami/minio", "All 2 · *Images 2", ""},
		{"/?q=library&type=helm", "", "All 3 · Images 3 · *Helm charts 0", "No Helm charts match “library”"},
	} {
		body := get(t, newTestServer(t, "/"), "/", c.target, false).Body.String()
		if got := strings.Join(rowRepos(body), " "); got != c.rows {
			t.Errorf("%s rows:\n got %s\nwant %s", c.target, got, c.rows)
		}
		if got := chipText(body); got != c.chips {
			t.Errorf("%s chips:\n got %s\nwant %s", c.target, got, c.chips)
		}
		if c.empty != "" && !strings.Contains(body, c.empty) {
			t.Errorf("%s lacks %q", c.target, c.empty)
		}
		// Filtered rows render loaded, and don't add up to the tag total.
		filtered := strings.Contains(c.target, "type=") && c.target != "/?type=nope"
		if strings.Contains(body, "aria-busy") == filtered || strings.Contains(body, "data-repo-list") == filtered {
			t.Errorf("%s: rows loaded or marked for the tag total when they shouldn't be", c.target)
		}
		_, main, _ := strings.Cut(body, "<main") // the layout's own scripts are allowed
		checkMarkup(t, main)
	}
}

func TestCatalogTypeKinds(t *testing.T) {
	s := newTestServer(t, "/")
	s.Src = retyped{Source: s.Src, artifacts: []string{"myteam/api"}}
	body := get(t, s, "/", "/?type=signature", false).Body.String()
	if got := strings.Join(rowRepos(body), " "); got != sigRepo {
		t.Errorf("signatures: %s", got)
	}
	if got, want := chipText(body), "All 9 · Images 6 · Helm charts 1 · *Signatures 1 · Other artifacts 1"; got != want {
		t.Errorf("chips:\n got %s\nwant %s", got, want)
	}
	body = get(t, s, "/", "/?type=artifact", false).Body.String()
	if got := strings.Join(rowRepos(body), " "); got != "myteam/api" {
		t.Errorf("artifacts: %s", got)
	}
}

func TestCatalogTypeKeptInLinks(t *testing.T) {
	body := get(t, newTestServer(t, "/"), "/", "/?q=a&type=image&sort=tags", false).Body.String()
	contains(t, body,
		`<a href="/?q=a&amp;sort=tags" class=`,                             // the All chip
		`<a href="/?q=a&amp;sort=tags&amp;type=image" aria-current="true"`, // the Images chip
		`<a href="/?q=a&amp;sort=name-desc&amp;type=image" class=`,         // the sort menu
		`<section id="repo-list" hx-get="/?sort=tags&amp;type=image"`,      // as you type
		`<input type="hidden" name="type" value="image">`,                  // the header search
	)
	// Filtered, then sorted by tag count; ties stay A–Z.
	if got, want := strings.Join(rowRepos(body), " "), "library/nginx library/postgres library/redis myteam/api bitnami/kafka bitnami/minio myteam/web/frontend"; got != want {
		t.Errorf("rows:\n got %s\nwant %s", got, want)
	}
}

func TestRepoTypes(t *testing.T) {
	s := newTestServer(t, "/")
	all := cardTags(get(t, s, "/", "/library/nginx?tab=tags", false).Body.String())
	sig := all[2]
	images := strings.Join(slices.DeleteFunc(slices.Clone(all), func(t string) bool { return t == sig }), " ")
	for _, c := range []struct{ target, tags, chips, empty string }{
		// Unfiltered: signatures are known by name, the rest by the header tag.
		{"/library/nginx?tab=tags", strings.Join(all, " "), "*All 15 · Images · Signatures 1", ""},
		{"/library/nginx?type=image", images, "All 15 · *Images 14 · Signatures 1", ""},
		{"/library/nginx?type=signature", sig, "All 15 · Images 14 · *Signatures 1", ""},
		{"/library/nginx?type=helm", "", "All 15 · Images 14 · *Helm charts 0 · Signatures 1", "No Helm charts here"},
		{"/library/nginx?type=artifact", "", "All 15 · Images 14 · Signatures 1 · *Other artifacts 0", "No artifacts here"},
		{"/charts/podinfo?tab=tags", "6.7.1 6.7.0", "*All 2 · Helm charts", ""},
		{"/charts/podinfo?type=helm", "6.7.1 6.7.0", "All 2 · *Helm charts 2", ""},
		{"/charts/podinfo?type=image", "", "All 2 · *Images 0 · Helm charts 2", "No images here"},
		// With the name filter.
		{"/library/nginx?q=alpine&type=image", "1.27.3-alpine 1.27.2-alpine 1.26.2-alpine", "All 3 · *Images 3", ""},
		{"/library/nginx?q=alpine&type=signature", "", "All 3 · Images 3 · *Signatures 0", "No signatures match “alpine”"},
	} {
		body := get(t, s, "/", c.target, false).Body.String()
		if got := strings.Join(cardTags(body), " "); got != c.tags {
			t.Errorf("%s tags:\n got %s\nwant %s", c.target, got, c.tags)
		}
		if got := chipText(body); got != c.chips {
			t.Errorf("%s chips:\n got %s\nwant %s", c.target, got, c.chips)
		}
		if c.empty != "" && !strings.Contains(body, c.empty) {
			t.Errorf("%s lacks %q", c.target, c.empty)
		}
		_, main, _ := strings.Cut(body, "<main") // the layout's own scripts are allowed
		checkMarkup(t, main)
	}

	s.Src = retyped{Source: s.Src, artifacts: []string{"myteam/api:pr-218"}}
	body := get(t, s, "/", "/myteam/api?type=artifact", false).Body.String()
	if got := strings.Join(cardTags(body), " "); got != "pr-218" {
		t.Errorf("artifacts: %s", got)
	}
	// v1.3.1, whose manifest is missing, counts only under All.
	if got, want := chipText(body), "All 5 · Images 3 · *Other artifacts 1"; got != want {
		t.Errorf("chips:\n got %s\nwant %s", got, want)
	}
}

func TestRepoTypeKeptInLinks(t *testing.T) {
	s := newTestServer(t, "/")
	s.Src = &manyTags{Source: s.Src, n: 250}
	body := get(t, s, "/", "/many?type=image&sort=name&view=list&page=2", false).Body.String()
	contains(t, body,
		`href="/many?page=3&amp;sort=name&amp;tab=tags&amp;type=image&amp;view=list"`, // Next
		`hx-get="/many?fragment=row&amp;page=2&amp;sort=name&amp;tag=`,                // a row's details…
		`&amp;type=image&amp;view=list"`,                                              // …on this page
		`<input type="hidden" name="type" value="image">`,                             // the filter
		`<a href="/many?sort=size&amp;tab=tags&amp;type=image&amp;view=list" class=`,  // the sort menu
		`<a href="/many?sort=name&amp;tab=tags&amp;view=list" class=`,                 // the All chip, back to page 1
	)

	rec := get(t, s, "/", "/library/nginx?q=1.27&type=image&sort=size&fragment=tags", true)
	if u := rec.Header().Get("HX-Replace-Url"); u != "/library/nginx?q=1.27&sort=size&tab=tags&type=image" {
		t.Errorf("HX-Replace-Url = %q", u)
	}
	// Filtered, then sorted by size; tags of one image tie and keep version order.
	if got, want := strings.Join(cardTags(rec.Body.String()), " "), "1.27.3 1.27 1.27.2 1.27.3-alpine 1.27.2-alpine"; got != want {
		t.Errorf("tags:\n got %s\nwant %s", got, want)
	}
	if got, want := chipText(rec.Body.String()), "All 5 · *Images 5"; got != want {
		t.Errorf("fragment chips: %s, want %s", got, want)
	}
	if rec := get(t, s, "/", "/many?fragment=row&page=2&type=image&tag=v150", true); rec.Code != 200 {
		t.Errorf("row on the filtered page 2: status %d", rec.Code)
	}
}

func TestTypeFilterGivesUp(t *testing.T) {
	defer func(d time.Duration) { sortTimeout = d }(sortTimeout)
	sortTimeout = time.Millisecond

	s := newTestServer(t, "/")
	s.Src = slowSource{s.Src}
	body := get(t, s, "/", "/?type=helm", false).Body.String()
	contains(t, body, "took too long to describe every repository, so this list isn&#39;t filtered by type")
	if n := len(rowRepos(body)); n != 8 {
		t.Errorf("%d rows, want all 8", n)
	}
	body = get(t, s, "/", "/library/nginx?type=signature", false).Body.String()
	contains(t, body, "took too long to describe every tag, so this list isn&#39;t filtered by type")
	if n := len(cardTags(body)); n != 15 {
		t.Errorf("%d tags, want all 15", n)
	}
}
