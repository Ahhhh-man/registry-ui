package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/fake"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// getPage serves path through the router at base and returns the status and body.
func getPage(t *testing.T, s *Server, base, path string, htmx bool) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	web.New(base, s.Handlers()).ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func resolve(t *testing.T, repo, ref string) registry.Manifest {
	t.Helper()
	m, err := fake.New().Resolve(t.Context(), repo, ref)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTagPage(t *testing.T) {
	nginx := resolve(t, "library/nginx", "1.27.3")
	amd64, arm64 := nginx.Platforms[0], nginx.Platforms[1]
	alpine := resolve(t, "library/nginx", "1.27.3-alpine")
	kafka := resolve(t, "bitnami/kafka", "latest")
	single := resolve(t, "myteam/api", "main")
	chart := resolve(t, "charts/podinfo", "6.7.1")
	redis := resolve(t, "library/redis", "7.2.6")
	sig := "sha256-" + strings.TrimPrefix(nginx.Digest, "sha256:") + ".sig"
	pull := "docker pull registry.example.com/library/nginx"
	created, err := fake.New().Config(t.Context(), "library/nginx", amd64.Config)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, path string
		htmx       bool
		status     int
		cards      int // platform or artifact cards
		want, not  []string
	}{
		{
			name: "multi-platform index", path: "/library/nginx:1.27.3", status: 200, cards: 6,
			want: []string{
				"<title>library/nginx:1.27.3 · registry.example.com</title>",
				`<span class="font-medium text-faint">nginx:</span>1.27.3</span>`,
				`<a href="/library/nginx" class="hover:text-text">nginx</a>`,
				// The pills: created with the time on hover, platforms with the main one's size, same-as links.
				`<span class="truncate">` + string(format.Time(created.Created, "Created ")) + "</span>", `UTC">Created 2 days ago</time>`,
				// Each platform card says when it was created.
				`<dt class="text-muted">Created</dt><dd class="min-w-0 truncate"><time datetime="`,
				">6 platforms · " + format.Size(amd64.Size) + " on amd64</span>",
				`>Same as <a href="/library/nginx:latest" class="font-medium text-accent-text hover:underline">latest</a>, <a href="/library/nginx:mainline"`,
				// The pull command, whole, with its Copy button.
				">" + pull + ":1.27.3</code>", `data-copy="` + pull + `:1.27.3" aria-label="Copy pull command"`,
				// And the bare reference, for everything that isn't docker.
				`data-copy="registry.example.com/library/nginx:1.27.3" class=`, `aria-live="polite">Copy reference</span>`,
				// A card per platform.
				"<h2 class=\"mb-3 text-base font-semibold\">Platforms</h2>",
				`<article id="platform-linux-arm-v7" class=`, ">linux/arm/v7</span></h3>", ">" + format.Size(arm64.Size) + "</span>",
				`<a href="/library/nginx@` + amd64.Digest + `" class="text-accent-text hover:underline">` + format.ShortDigest(amd64.Digest) + "</a>",
				">OCI image manifest</dd>",
				`data-copy="` + pull + "@" + amd64.Digest + `" aria-label="Copy pull command for linux/amd64"`,
				`data-copy="registry.example.com/library/nginx@` + amd64.Digest + `" aria-label="Copy reference for linux/amd64"`,
				`data-copy="` + arm64.Digest + `" aria-label="Copy digest of linux/arm64"`,
				// Technical details, closed.
				"<details class=", "Index digest</dt>", ">" + nginx.Digest + "</code>",
				"application/vnd.oci.image.index.v1&#43;json",
				"<dd>" + created.Created.UTC().Format("2006-01-02 15:04:05") + " UTC</dd>",
				"Layers and build history are coming",
			},
			not: []string{"Pushed", ">latest</span>", `aria-current="true"`, "fragment=platform", "<details open", `role="note"`},
		},
		{
			name: "latest gets its badge", path: "/library/nginx:latest", status: 200, cards: 6,
			want: []string{">latest</span>", `>Same as <a href="/library/nginx:mainline"`},
		},
		{
			name: "8 platforms", path: "/library/nginx:1.27.3-alpine", status: 200, cards: 8,
			want: []string{">8 platforms · " + format.Size(alpine.Platforms[0].Size) + " on amd64</span>", ">linux/riscv64</span></h3>"},
		},
		{
			name: "2 platforms", path: "/bitnami/kafka:latest", status: 200, cards: 2,
			want: []string{">2 platforms · " + format.Size(kafka.Platforms[0].Size) + " on amd64</span>", `href="/bitnami/kafka:3.8.1"`},
		},
		{
			name: "platform highlighted", path: "/library/nginx:1.27.3?platform=linux/arm64", status: 200, cards: 6,
			want: []string{"<!doctype html>", `<article id="platform-linux-arm64" aria-current="true"`},
		},
		{
			name: "unknown platform highlights none", path: "/library/nginx:1.27.3?platform=windows/amd64", status: 200, cards: 6,
			not: []string{`aria-current="true"`},
		},
		{
			name: "old fragment link gets the page", path: "/library/nginx:1.27.3?platform=linux/arm64&fragment=platform", htmx: true, status: 200, cards: 6,
			want: []string{"<!doctype html>", `id="platform-linux-arm64" aria-current="true"`},
		},
		{
			name: "single-platform", path: "/myteam/api:main", status: 200, cards: 1,
			want: []string{">1 platform · ", `UTC">Created today</time>`, "docker pull registry.example.com/myteam/api:main</code>", "docker pull registry.example.com/myteam/api@" + single.Digest, "Digest</dt>"},
			not:  []string{"Same as", "Index digest"},
		},
		{
			name: "digest of an index", path: "/library/nginx@" + nginx.Digest, status: 200, cards: 6,
			want: []string{
				`<span class="font-medium text-faint">nginx@</span>sha256:` + format.ShortDigest(nginx.Digest) + "</span>",
				">" + pull + "@" + nginx.Digest + "</code>", ">6 platforms · ", `>Same as <a href="/library/nginx:latest"`,
				`data-copy="registry.example.com/library/nginx@` + nginx.Digest + `" class=`,
			},
		},
		{
			name: "digest of one platform", path: "/library/nginx@" + arm64.Digest, status: 200, cards: 1,
			want: []string{">1 platform · ", ">linux/arm64</span></h3>", ">" + pull + "@" + arm64.Digest + "</code>"},
			not:  []string{"Same as", "linux/amd64"},
		},
		{
			name: "signature", path: "/library/nginx:" + sig, status: 200, cards: 1,
			want: []string{
				"</svg>signature</span>", ">Signature</span></h3>", "cosign signature, attestation or SBOM", sig[:19] + "….sig", ">1.2 KB</span>",
				"Belongs to</dt>", `<a href="/library/nginx@` + nginx.Digest + `" class="text-accent-text hover:underline">sha256:` + format.ShortDigest(nginx.Digest) + "</a>",
				// No pull box, so the card copies the reference.
				`data-copy="registry.example.com/library/nginx:` + sig + `" class=`, `aria-live="polite">Copy reference</span>`,
			},
			not: []string{"docker pull", "bg-code-bg", "Platforms</h2>", "Created"},
		},
		{
			name: "helm", path: "/charts/podinfo:6.7.1", status: 200, cards: 1,
			want: []string{
				"</svg>helm chart</span>", `<h2 class="sr-only">Contents</h2>`, ">Helm chart</span></h3>", ">14 KB</span>", ">OCI artifact</dd>",
				">helm pull oci://registry.example.com/charts/podinfo --version 6.7.1</code>", `data-copy="` + chart.Digest + `"`,
				`data-copy="registry.example.com/charts/podinfo:6.7.1" class=`, // no oci://
			},
			not: []string{"docker pull", "Platforms</h2>"},
		},
		{
			name: "some platforms missing", path: "/library/redis:7.2.6", status: 200, cards: 6,
			want: []string{
				// The count says what's missing, and arm64 speaks for the tag instead of amd64.
				">4 platforms (2 missing) · " + format.Size(redis.Platforms[1].Size) + " on arm64</span>", "Created 2 months ago",
				// Missing platforms come last, dimmed, with a note and only their digest to copy.
				`<article id="platform-linux-ppc64le" class="flex min-w-0 flex-col gap-4 rounded-lg border border-border p-5 aria-[current=true]:border-accent aria-[current=true]:ring-1 aria-[current=true]:ring-accent bg-surface">`,
				`<article id="platform-linux-amd64" class="flex min-w-0 flex-col gap-4 rounded-lg border border-border p-5 aria-[current=true]:border-accent aria-[current=true]:ring-1 aria-[current=true]:ring-accent border-dashed bg-panel text-muted">`,
				"This platform&#39;s manifest isn&#39;t in the registry.", `aria-label="Copy digest of linux/amd64"`,
				`aria-label="Copy reference for linux/arm64"`,
			},
			not: []string{"docker pull registry.example.com/library/redis@" + redis.Platforms[0].Digest, `data-copy="registry.example.com/library/redis@` + redis.Platforms[0].Digest, "Copy reference for linux/amd64", `href="/library/redis@` + redis.Platforms[0].Digest, "Not cached"},
		},
		{
			name: "missing platform highlighted", path: "/library/redis:7.2.6?platform=linux/amd64", status: 200, cards: 6,
			want: []string{`<article id="platform-linux-amd64" aria-current="true"`, "isn&#39;t in the registry"},
		},
		{
			name: "every platform missing", path: "/library/redis:7.0.15", status: 200, cards: 2,
			want: []string{">2 platforms, all missing</span>", "Platforms</h2>", ">docker pull registry.example.com/library/redis:7.0.15</code>", "border-dashed"},
			not:  []string{">artifact<", "Created", " B</span>", "docker pull registry.example.com/library/redis@"},
		},
		{
			name: "unknown tag", path: "/library/nginx:9.9.9", status: 404,
			want: []string{`<h1 class="font-medium text-text">Tag “9.9.9” doesn&#39;t exist</h1>`, `<a href="/library/nginx" class="mt-3`, "Back to library/nginx"},
		},
		{
			name: "unknown digest", path: "/library/nginx@sha256:" + strings.Repeat("0", 64), status: 404,
			want: []string{`<h1 class="font-medium text-text">Digest not found</h1>`, "Back to library/nginx"},
		},
		{
			name: "unknown repo", path: "/nope/nope:1", status: 404,
			want: []string{"Back to nope/nope"},
		},
	}
	s := newTestServer(t, "/")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := getPage(t, s, "/", c.path, c.htmx)
			if status != c.status {
				t.Errorf("status = %d, want %d", status, c.status)
			}
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("lacks %q", w)
				}
			}
			for _, n := range c.not {
				if strings.Contains(body, n) {
					t.Errorf("has %q", n)
				}
			}
			if n := strings.Count(body, "<article "); n != c.cards {
				t.Errorf("%d cards, want %d", n, c.cards)
			}
			if strings.Count(body, `aria-current="true"`) > 1 {
				t.Error("more than one card highlighted")
			}
			_, page, _ := strings.Cut(body, "</head>") // the layout's own scripts are allowed
			checkMarkup(t, page)
			if t.Failed() {
				t.Log(body)
			}
		})
	}
}

// References use the pull host, like the pull commands do.
func TestReferencesUsePullHost(t *testing.T) {
	amd64 := resolve(t, "library/nginx", "1.27.3").Platforms[0]
	s := newTestServer(t, "/")
	s.PullHost = "cr.example.com:5000"
	for path, want := range map[string][]string{
		"/library/nginx:1.27.3":    {`data-copy="cr.example.com:5000/library/nginx:1.27.3" class=`, `data-copy="cr.example.com:5000/library/nginx@` + amd64.Digest + `" aria-label="Copy reference for linux/amd64"`},
		"/library/nginx?view=list": {`data-ref="cr.example.com:5000/library/nginx:1.27.3"`},
	} {
		_, body := getPage(t, s, "/", path, false)
		contains(t, body, want...)
		if strings.Contains(body, `"registry.example.com/`) {
			t.Errorf("%s has a reference that ignores the pull host", path)
		}
	}
}

func TestTagPageBasePath(t *testing.T) {
	nginx := resolve(t, "library/nginx", "1.27.3")
	sig := "sha256-" + strings.TrimPrefix(nginx.Digest, "sha256:") + ".sig"
	s := newTestServer(t, "/registry/")
	for path, wants := range map[string][]string{
		"/registry/library/nginx:1.27.3": {
			`<a href="/registry/" class="hover:text-text">Repositories</a>`, `<a href="/registry/?q=library/" class="hover:text-text">library</a>`,
			`<a href="/registry/library/nginx" class="hover:text-text">nginx</a>`,
			`<a href="/registry/library/nginx:latest" class=`,
			`<a href="/registry/library/nginx@` + nginx.Platforms[1].Digest + `" class=`,
		},
		"/registry/library/nginx:" + sig:          {`<a href="/registry/library/nginx@` + nginx.Digest + `" class=`},
		"/registry/library/nginx@" + nginx.Digest: {`<a href="/registry/?q=library/" class="hover:text-text">library</a>`},
	} {
		_, body := getPage(t, s, "/registry/", path, false)
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %q", path, w)
			}
		}
	}
}

// counting counts registry calls, and lists no tags so "Same as" makes none.
type counting struct {
	registry.Source
	resolves, configs atomic.Int32
}

func (c *counting) Tags(context.Context, string) ([]string, error) {
	return nil, errors.New("not listed")
}

func (c *counting) Resolve(ctx context.Context, repo, ref string) (registry.Manifest, error) {
	c.resolves.Add(1)
	return c.Source.Resolve(ctx, repo, ref)
}

func (c *counting) Config(ctx context.Context, repo, digest string) (registry.ImageConfig, error) {
	c.configs.Add(1)
	return c.Source.Config(ctx, repo, digest)
}

// The page resolves the tag once and fetches one config per platform card.
func TestTagPageCalls(t *testing.T) {
	for path, configs := range map[string]int32{
		"/library/nginx:1.27.3":                       6,
		"/library/nginx:1.27.3?platform=linux/arm/v7": 6,
		"/myteam/api:main":                            1,
		"/library/redis:7.2.6":                        4, // none for the missing platforms
		"/library/redis:7.0.15":                       0,
		"/charts/podinfo:6.7.1":                       0,
	} {
		s := newTestServer(t, "/")
		c := &counting{Source: fake.New()}
		s.Src = c
		if status, _ := getPage(t, s, "/", path, false); status != http.StatusOK {
			t.Fatalf("%s: status %d", path, status)
		}
		if r, n := c.resolves.Load(), c.configs.Load(); r != 1 || n != configs {
			t.Errorf("%s: %d resolves and %d configs, want 1 and %d", path, r, n, configs)
		}
	}
}

// extraTags adds tags to a repo, to push it past sameAsLimit.
type extraTags struct {
	registry.Source
	extra int
}

func (m extraTags) Tags(ctx context.Context, repo string) ([]string, error) {
	tags, err := m.Source.Tags(ctx, repo)
	for i := range m.extra {
		tags = append(tags, fmt.Sprint("extra-", i))
	}
	return tags, err
}

func TestSameAsSkipsBigRepos(t *testing.T) {
	s := newTestServer(t, "/")
	tags, _ := s.Src.Tags(t.Context(), "library/nginx")
	for extra, want := range map[int]bool{sameAsLimit - len(tags): true, sameAsLimit - len(tags) + 1: false} {
		s.Src = extraTags{fake.New(), extra}
		_, body := getPage(t, s, "/", "/library/nginx:1.27.3", false)
		if got := strings.Contains(body, "Same as"); got != want {
			t.Errorf("%d tags: Same as shown = %v, want %v", len(tags)+extra, got, want)
		}
	}
}

// Against a real registry, the index's attestation manifest isn't a platform.
func TestTagPageHidesAttestations(t *testing.T) {
	r := registrytest.New(t)
	api, err := registry.NewAPI(r.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, "/")
	s.Src = api
	att, err := r.Attestation.Digest()
	if err != nil {
		t.Fatal(err)
	}
	status, body := getPage(t, s, "/", "/"+registrytest.RepoIndex+":"+registrytest.TagVersion, false)
	if status != http.StatusOK || !strings.Contains(body, ">3 platforms · ") || strings.Count(body, "<article ") != 3 || strings.Contains(body, "unknown") || strings.Contains(body, att.String()) {
		t.Errorf("status %d; want 3 platforms and no attestation in\n%s", status, body)
	}
}

// In storage mode, a missing platform is one nobody pulled through the cache.
func TestTagPageNotCached(t *testing.T) {
	s := newTestServer(t, "/")
	s.Storage = true
	_, body := getPage(t, s, "/", "/library/redis:7.2.6", false)
	if strings.Count(body, "Not cached. Nobody has pulled this platform through the cache yet.") != 2 || strings.Contains(body, "isn&#39;t in the registry") {
		t.Errorf("want two not-cached notes in\n%s", body)
	}
}

// Against a real registry, in both modes, a platform the registry lacks is
// counted as missing rather than failing the tag.
func TestMissingPlatformsInRegistry(t *testing.T) {
	r := registrytest.New(t)
	api, err := registry.NewAPI(r.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	st := registrytest.NewStorage(t)
	storage, err := registry.NewStorage(st.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]registry.Source{"api": api, "storage": storage} {
		s := newTestServer(t, "/")
		s.Src = src
		for repo, want := range map[string][]string{
			registrytest.RepoSomeMissing: {"2 platforms (1 missing)", "<dt>Platforms</dt>", ` (1 missing)">`, `<time datetime="`, ` UTC" data-title="Latest created {}">`},
			registrytest.RepoAllMissing:  {"3 platforms, all missing"},
		} {
			req := httptest.NewRequest("GET", "/?fragment=row&view=detailed&repo="+repo, nil)
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			s.Catalog(rec, req, web.Ref{})
			// The row names the platforms it has, so only the page counts them.
			for _, w := range want[min(1, len(want)-1):] {
				if body := rec.Body.String(); !strings.Contains(body, w) || strings.Contains(body, "Couldn't load") {
					t.Errorf("%s: catalog row for %s lacks %q:\n%s", name, repo, w, body)
				}
			}
			status, body := getPage(t, s, "/", "/"+repo+":"+registrytest.TagVersion, false)
			if status != http.StatusOK || strings.Count(body, "<article ") != 3 || !strings.Contains(body, want[0]) {
				t.Errorf("%s: tag page for %s: status %d\n%s", name, repo, status, body)
			}
		}
	}
}

// Against a real registry, in both modes, a listed tag whose manifest is
// gone shows as "manifest missing" wherever it would show, rather than as
// an error or a tag that doesn't exist.
func TestMissingManifestInRegistry(t *testing.T) {
	r := registrytest.New(t)
	api, err := registry.NewAPI(r.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	st := registrytest.NewStorage(t)
	storage, err := registry.NewStorage(st.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	// The dangling tag was touched last, so it's first by update.
	updated := time.Now().Add(-time.Hour)
	st.SetTagTime(t, registrytest.RepoDangling, registrytest.TagLatest, updated)
	st.SetTagTime(t, registrytest.RepoDangling, registrytest.TagVersion, updated.Add(-365*24*time.Hour))

	repo, tag := "/"+registrytest.RepoDangling, registrytest.TagLatest
	for name, src := range map[string]registry.Source{"api": api, "storage": storage} {
		s := newTestServer(t, "/")
		s.Src = src
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

		// The catalog row counts the tags and leaves the rest out, without failing.
		row := get(t, s, "/", "/?fragment=row&repo="+registrytest.RepoDangling, true).Body.String()
		check("catalog row", row, []string{">2 tags</span>"}, []string{"Couldn't load", "platform", "Latest created"})

		// A card or list row has the badge and no digest, size or pull command.
		card := get(t, s, "/", repo+"?fragment=card&tag="+tag, true)
		check("card", card.Body.String(), []string{"</svg>manifest missing</span>", ">latest</span>"}, []string{"sha256:", "docker pull", "Created", "<table"})
		if card.Code != http.StatusOK {
			t.Errorf("%s: card status %d", name, card.Code)
		}
		list := get(t, s, "/", repo+"?view=list&fragment=row&tag="+tag, true).Body.String()
		check("list row", list, []string{`data-tag="latest" data-pull="" data-ref=""`, `aria-label="Select latest"`, "</svg>manifest missing</span>"}, []string{"sha256:", "data-copy", "Unknown"})
		if name == "storage" {
			check("list row", list, []string{string(format.Time(updated))}, nil)
		}
		// "Same as" leaves it out.
		other := get(t, s, "/", repo+"?fragment=card&tag="+registrytest.TagVersion, true).Body.String()
		check("other card", other, []string{"docker pull"}, []string{"Same as"})

		// The tag page explains, with a 404.
		status, body := getPage(t, s, "/", repo+":"+tag, false)
		check("tag page", body, []string{"Manifest missing", "The registry still lists this tag, but the manifest it points to is gone"}, []string{"doesn&#39;t exist"})
		if status != http.StatusNotFound {
			t.Errorf("%s: tag page status %d, want 404", name, status)
		}
		_, body = getPage(t, s, "/", repo+":nope", false)
		check("unknown tag", body, []string{"doesn&#39;t exist"}, []string{"Manifest missing"})

		// Overview summarises latest, so it says the same.
		body = get(t, s, "/", repo, false).Body.String()
		check("overview", body, []string{"Manifest missing. The registry still lists this tag"}, []string{"docker pull"})

		// The created and size sorts put it last; type filters leave it out.
		for _, tc := range []struct{ query, want string }{
			{"?tab=tags", "latest 1.0.0"},
			{"?sort=created", "1.0.0 latest"},
			{"?sort=size", "1.0.0 latest"},
			{"?type=image", "1.0.0"},
			{"?type=helm", ""},
			{"?type=artifact", ""},
		} {
			if got := strings.Join(cardTags(get(t, s, "/", repo+tc.query, false).Body.String()), " "); got != tc.want {
				t.Errorf("%s: tags%s = %q, want %q", name, tc.query, got, tc.want)
			}
		}
		// Its link's time still counts in storage mode.
		if name == "storage" {
			if got := strings.Join(cardTags(get(t, s, "/", repo+"?sort=updated", false).Body.String()), " "); got != "latest 1.0.0" {
				t.Errorf("tags?sort=updated = %q", got)
			}
		}

		// In the catalog, a repository whose latest is missing has no type, and no date.
		if got := rowRepos(get(t, s, "/", "/?type=image", false).Body.String()); slices.Contains(got, registrytest.RepoDangling) {
			t.Errorf("%s: ?type=image lists %s: %v", name, registrytest.RepoDangling, got)
		}
		if got := rowRepos(get(t, s, "/", "/?sort=created", false).Body.String()); len(got) == 0 || got[len(got)-1] != registrytest.RepoDangling {
			t.Errorf("%s: ?sort=created should put %s last: %v", name, registrytest.RepoDangling, got)
		}
	}
}

// failResolve is a registry that fails every manifest lookup with err.
type failResolve struct {
	registry.Source
	err error
}

func (s failResolve) Resolve(context.Context, string, string) (registry.Manifest, error) {
	return registry.Manifest{}, s.err
}

// Only a not-found makes a listed tag's manifest missing. A 401, 403 or
// 5xx is still an error.
func TestResolveErrorsAreNotMissingManifests(t *testing.T) {
	for _, err := range []error{registry.ErrUnauthorized, registry.ErrForbidden, registry.ErrUnavailable} {
		s := newTestServer(t, "/")
		s.Src = failResolve{s.Src, fmt.Errorf("manifest: %w", err)}
		status, body := getPage(t, s, "/", "/myteam/api:main", false)
		if status == http.StatusNotFound || strings.Contains(body, "Manifest missing") {
			t.Errorf("%v: tag page: status %d\n%s", err, status, body)
		}
		rec := get(t, s, "/", "/myteam/api?fragment=card&tag=main", true)
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "manifest missing") {
			t.Errorf("%v: card: status %d\n%s", err, rec.Code, rec.Body.String())
		}
		if body := get(t, s, "/", "/?fragment=row&repo=myteam/api", true).Body.String(); !strings.Contains(body, "Couldn't load") {
			t.Errorf("%v: catalog row doesn't fail:\n%s", err, body)
		}
	}
}
