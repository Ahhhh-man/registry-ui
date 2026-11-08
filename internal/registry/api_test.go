package registry_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

func newAPI(t *testing.T, url string) *registry.API {
	t.Helper()
	a, err := registry.NewAPI(url, false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// want is the summary Resolve should give for img.
func want(t *testing.T, img v1.Image) registry.Platform {
	t.Helper()
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	m, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	size := m.Config.Size
	for _, l := range m.Layers {
		size += l.Size
	}
	return registry.Platform{OS: cf.OS, Arch: cf.Architecture, Variant: cf.Variant, Digest: d.String(), Config: m.Config.Digest.String(), Size: size}
}

func digest(t *testing.T, x interface{ Digest() (v1.Hash, error) }) string {
	t.Helper()
	d, err := x.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

func TestAPICatalogAndTags(t *testing.T) {
	r := registrytest.New(t)
	a, ctx := newAPI(t, r.URL), t.Context()

	repos, err := a.Catalog(ctx)
	wantRepos := []string{registrytest.RepoHelm, registrytest.RepoSingle, registrytest.RepoIndex, registrytest.RepoNested, registrytest.RepoPaged}
	if err != nil || !slices.Equal(repos, wantRepos) {
		t.Errorf("Catalog = %v, %v; want %v", repos, err, wantRepos)
	}

	// Four pages of registrytest.PageSize.
	tags, err := a.Tags(ctx, registrytest.RepoPaged)
	slices.Sort(tags)
	wantTags := slices.Sorted(slices.Values(registrytest.PagedTags))
	if err != nil || !slices.Equal(tags, wantTags) {
		t.Errorf("Tags = %v, %v; want %v", tags, err, wantTags)
	}

	tags, err = a.Tags(ctx, registrytest.RepoIndex)
	if err != nil || len(tags) != 3 || !slices.Contains(tags, r.SignatureTag) {
		t.Errorf("Tags(%s) = %v, %v; want 1.0.0, latest and the signature", registrytest.RepoIndex, tags, err)
	}
}

func TestAPIResolveIndex(t *testing.T) {
	r := registrytest.New(t)
	a, ctx := newAPI(t, r.URL), t.Context()

	m, err := a.Resolve(ctx, registrytest.RepoIndex, registrytest.TagVersion)
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != registry.KindIndex || m.MediaType != string(types.OCIImageIndex) || m.Digest != digest(t, r.Index) || m.Size != 0 {
		t.Errorf("index = %+v", m)
	}
	// The attestation child is left out.
	var platforms []registry.Platform
	for _, img := range r.Platforms {
		platforms = append(platforms, want(t, img))
	}
	if !slices.Equal(m.Platforms, platforms) {
		t.Errorf("platforms = %+v\nwant %+v", m.Platforms, platforms)
	}
	if v7 := m.Platforms[2]; v7.Arch != "arm" || v7.Variant != "v7" {
		t.Errorf("third platform = %+v, want linux/arm/v7", v7)
	}

	// The alias resolves to the same index.
	if latest, err := a.Resolve(ctx, registrytest.RepoIndex, registrytest.TagLatest); err != nil || latest.Digest != m.Digest {
		t.Errorf("latest = %v, %v; want %s", latest.Digest, err, m.Digest)
	}

	// So does its digest, and each platform resolves on its own.
	if byDigest, err := a.Resolve(ctx, registrytest.RepoIndex, m.Digest); err != nil || byDigest.Kind != registry.KindIndex {
		t.Errorf("by digest = %+v, %v", byDigest, err)
	}
	one, err := a.Resolve(ctx, registrytest.RepoIndex, platforms[1].Digest)
	if err != nil || one.Kind != registry.KindImage || !slices.Equal(one.Platforms, platforms[1:2]) {
		t.Errorf("arm64 by digest = %+v, %v", one, err)
	}

	cfg, err := a.Config(ctx, registrytest.RepoIndex, platforms[0].Config)
	if err != nil || !cfg.Created.Equal(registrytest.PlatformsCreated) || !maps.Equal(cfg.Labels, registrytest.PlatformsLabels) {
		t.Errorf("config = %+v, %v", cfg, err)
	}
}

func TestAPIResolveSingle(t *testing.T) {
	r := registrytest.New(t)
	a, ctx := newAPI(t, r.URL), t.Context()
	p := want(t, r.Single)

	for repo, tag := range map[string]string{registrytest.RepoSingle: registrytest.TagSingle, registrytest.RepoNested: registrytest.TagNested} {
		m, err := a.Resolve(ctx, repo, tag)
		if err != nil || m.Digest != p.Digest || m.MediaType != string(types.OCIManifestSchema1) ||
			m.Kind != registry.KindImage || m.Size != p.Size || !slices.Equal(m.Platforms, []registry.Platform{p}) {
			t.Errorf("%s:%s = %+v, %v; want one platform %+v", repo, tag, m, err, p)
		}
	}

	cfg, err := a.Config(ctx, registrytest.RepoSingle, p.Config)
	if err != nil || !cfg.Created.Equal(registrytest.SingleCreated) || len(cfg.Labels) != 0 {
		t.Errorf("config = %+v, %v", cfg, err)
	}
}

func TestAPIKinds(t *testing.T) {
	r := registrytest.New(t)
	a, ctx := newAPI(t, r.URL), t.Context()

	for _, tc := range []struct {
		name, repo, ref string
		kind            registry.Kind
		img             v1.Image
	}{
		{"signature tag", registrytest.RepoIndex, r.SignatureTag, registry.KindSignature, r.Signature},
		{"signature by digest", registrytest.RepoIndex, digest(t, r.Signature), registry.KindSignature, r.Signature},
		{"helm", registrytest.RepoHelm, registrytest.TagHelm, registry.KindHelm, r.Helm},
		{"attestation by digest", registrytest.RepoIndex, digest(t, r.Attestation), registry.KindSignature, r.Attestation},
	} {
		m, err := a.Resolve(ctx, tc.repo, tc.ref)
		if err != nil || m.Kind != tc.kind || len(m.Platforms) != 0 || m.Size != want(t, tc.img).Size {
			t.Errorf("%s: %+v, %v; want kind %v with no platforms", tc.name, m, err, tc.kind)
		}
	}
}

func TestAPINotFound(t *testing.T) {
	r := registrytest.New(t)
	a, ctx := newAPI(t, r.URL), t.Context()

	_, err1 := a.Tags(ctx, "nope/missing")
	_, err2 := a.Resolve(ctx, registrytest.RepoIndex, "0.0.0")
	_, err3 := a.Resolve(ctx, "Not A Repo", "latest")
	_, err4 := a.Config(ctx, registrytest.RepoIndex, "sha256:00")
	_, err5 := a.Config(ctx, registrytest.RepoIndex, "sha256:"+strings.Repeat("0", 64))
	for i, err := range []error{err1, err2, err3, err4, err5} {
		if !errors.Is(err, registry.ErrNotFound) {
			t.Errorf("%d: got %v, want ErrNotFound", i+1, err)
		}
	}
}

func TestAPIBasicAuth(t *testing.T) {
	r := registrytest.NewWithBasicAuth(t, "alice", "secret")
	a := newAPI(t, r.URL)

	for _, ctx := range []context.Context{t.Context(), registry.WithCredentials(t.Context(), "alice", "wrong")} {
		_, err := a.Catalog(ctx)
		var ue *registry.UnauthorizedError
		if !errors.As(err, &ue) || !ue.Challenged || !errors.Is(err, registry.ErrUnauthorized) {
			t.Errorf("got %v, want a challenged UnauthorizedError", err)
		}
	}

	ctx := registry.WithCredentials(t.Context(), "alice", "secret")
	if _, err := a.Catalog(ctx); err != nil {
		t.Errorf("catalog with credentials: %v", err)
	}
	m, err := a.Resolve(ctx, registrytest.RepoIndex, registrytest.TagLatest)
	if err != nil || len(m.Platforms) != 3 {
		t.Fatalf("resolve with credentials: %+v, %v", m, err)
	}
	if _, err := a.Config(ctx, registrytest.RepoIndex, m.Platforms[0].Config); err != nil {
		t.Errorf("config with credentials: %v", err)
	}
}

func TestAPIErrors(t *testing.T) {
	status := func(code int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	for _, tc := range []struct {
		name, url string
		want      error
	}{
		{"403", status(http.StatusForbidden), registry.ErrForbidden},
		{"500", status(http.StatusInternalServerError), registry.ErrUnavailable},
		{"401 without a challenge", status(http.StatusUnauthorized), registry.ErrUnauthorized},
		{"connection refused", closed.URL, registry.ErrUnavailable},
	} {
		_, err := newAPI(t, tc.url).Catalog(t.Context())
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
		if ue := (*registry.UnauthorizedError)(nil); errors.As(err, &ue) && ue.Challenged {
			t.Errorf("%s: reported a challenge the registry never sent", tc.name)
		}
	}
}

func TestNewAPI(t *testing.T) {
	for _, u := range []string{"", "registry:5000", "ftp://registry", "http://", "http://registry/sub/path"} {
		if _, err := registry.NewAPI(u, false); err == nil {
			t.Errorf("NewAPI(%q) should fail", u)
		}
	}
	for _, u := range []string{"http://registry:5000", "https://registry.example.com/"} {
		if _, err := registry.NewAPI(u, true); err != nil {
			t.Errorf("NewAPI(%q): %v", u, err)
		}
	}
}
