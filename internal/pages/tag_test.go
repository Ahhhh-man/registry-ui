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
	single := resolve(t, "myteam/api", "main")
	sig := "sha256-" + strings.TrimPrefix(nginx.Digest, "sha256:") + ".sig"
	pull := "docker pull registry.example.com/library/nginx"

	cases := []struct {
		name, path string
		htmx       bool
		status     int
		want, not  []string
	}{
		{
			name: "multi-arch", path: "/library/nginx:1.27.3", status: 200,
			want: []string{
				"<title>library/nginx:1.27.3 · registry.example.com</title>",
				`<span class="font-medium text-faint">nginx:</span>1.27.3</span>`,
				`<a href="/library/nginx" class="hover:text-text">nginx</a>`,
				"Created 2 days ago · ", ">6 platforms<", ">Same as latest, mainline, 1.27<",
				`data-copy="` + nginx.Digest + `"`,
				pull + ":1.27.3",
				`href="/library/nginx:1.27.3?platform=linux/arm/v7"`,
				`href="/library/nginx:1.27.3?platform=linux/amd64" hx-get="/library/nginx:1.27.3?platform=linux/amd64&amp;fragment=platform"`,
				pull + "@" + amd64.Digest, size(amd64), size(arm64),
				"application/vnd.oci.image.manifest.v1&#43;json",
				"Layers and build history are coming",
			},
			not: []string{"Pushed", ">latest</span>"},
		},
		{
			name: "latest gets its badge", path: "/library/nginx:latest", status: 200,
			want: []string{">latest</span>", ">Same as mainline, 1.27.3, 1.27<"},
		},
		{
			name: "platform switch", path: "/library/nginx:1.27.3?platform=linux/arm64", status: 200,
			want: []string{"<!doctype html>", pull + "@" + arm64.Digest, `aria-current="true"`},
			not:  []string{pull + "@" + amd64.Digest},
		},
		{
			name: "platform fragment", path: "/library/nginx:1.27.3?platform=linux/arm64&fragment=platform", htmx: true, status: 200,
			want: []string{`<div id="platforms"`, pull + "@" + arm64.Digest},
			not:  []string{"<!doctype html>", "Same as"},
		},
		{
			name: "unknown platform falls back to amd64", path: "/library/nginx:1.27.3?platform=windows/amd64", status: 200,
			want: []string{pull + "@" + amd64.Digest},
		},
		{
			name: "single-arch", path: "/myteam/api:main", status: 200,
			want: []string{">1 platform<", "docker pull registry.example.com/myteam/api@" + single.Digest, "Created today · "},
			not:  []string{"Same as"},
		},
		{
			name: "digest of an index", path: "/library/nginx@" + nginx.Digest, status: 200,
			want: []string{
				`<span class="font-medium text-faint">nginx@</span>sha256:` + format.ShortDigest(nginx.Digest) + "</span>",
				pull + "@" + nginx.Digest, ">6 platforms<", "Same as latest, mainline, 1.27.3, 1.27<",
			},
		},
		{
			name: "digest of one platform", path: "/library/nginx@" + arm64.Digest, status: 200,
			want: []string{">1 platform<", ">linux/arm64</h2>", pull + "@" + arm64.Digest},
			not:  []string{"Same as", "linux/amd64"},
		},
		{
			name: "signature", path: "/library/nginx:" + sig, status: 200,
			want: []string{"</svg>signature</span>", "Cosign signature", "not a runnable image", sig[:19] + "….sig", "1.2 KB"},
			not:  []string{"docker pull", "Platforms", "Created"},
		},
		{
			name: "helm", path: "/charts/podinfo:6.7.1", status: 200,
			want: []string{"</svg>helm chart</span>", "Helm chart", "helm pull oci://registry.example.com/charts/podinfo --version 6.7.1", "14 KB"},
			not:  []string{"docker pull", "Platforms"},
		},
		{
			name: "unknown tag", path: "/library/nginx:9.9.9", status: 404,
			want: []string{"Tag “9.9.9” doesn&#39;t exist", `<a href="/library/nginx" class="mt-3`, "Back to library/nginx"},
		},
		{
			name: "unknown digest", path: "/library/nginx@sha256:" + strings.Repeat("0", 64), status: 404,
			want: []string{"Digest not found", "Back to library/nginx"},
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
			if t.Failed() {
				t.Log(body)
			}
		})
	}
}

func size(p registry.Platform) string { return ">" + format.Size(p.Size) + "<" }

func TestTagPageBasePath(t *testing.T) {
	_, body := getPage(t, newTestServer(t, "/registry/"), "/registry/", "/registry/library/nginx:1.27.3", false)
	for _, w := range []string{
		`<a href="/registry/" class="hover:text-text">Repositories</a>`,
		`<a href="/registry/library/nginx" class="hover:text-text">nginx</a>`,
		`href="/registry/library/nginx:1.27.3?platform=linux/arm64"`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("lacks %q", w)
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
	if status != http.StatusOK || !strings.Contains(body, ">3 platforms<") || strings.Contains(body, "unknown") || strings.Contains(body, att.String()) {
		t.Errorf("status %d; want 3 platforms and no attestation in\n%s", status, body)
	}
}
