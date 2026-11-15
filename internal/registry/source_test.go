package registry_test

import (
	"cmp"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

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
	return registry.Platform{OS: cf.OS, Arch: cf.Architecture, Variant: cf.Variant, Digest: d.String(), MediaType: string(m.MediaType), Config: m.Config.Digest.String(), Size: size}
}

func digest(t *testing.T, x interface{ Digest() (v1.Hash, error) }) string {
	t.Helper()
	d, err := x.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

// TestSource runs one suite against every mode, so they can't drift apart.
func TestSource(t *testing.T) {
	apiMode := func(t *testing.T) (registry.Source, *registrytest.Registry) {
		r := registrytest.New(t)
		return newAPI(t, r.URL), r
	}
	storageMode := func(t *testing.T) (registry.Source, *registrytest.Registry) { return newStorage(t) }
	cached := func(f func(*testing.T) (registry.Source, *registrytest.Registry)) func(*testing.T) (registry.Source, *registrytest.Registry) {
		return func(t *testing.T) (registry.Source, *registrytest.Registry) {
			s, r := f(t)
			return registry.NewCache(s), r
		}
	}
	for name, f := range map[string]func(*testing.T) (registry.Source, *registrytest.Registry){
		"api": apiMode, "storage": storageMode, "cached api": cached(apiMode), "cached storage": cached(storageMode),
	} {
		t.Run(name, func(t *testing.T) { testSource(t, f) })
	}
}

func testSource(t *testing.T, newSource func(t *testing.T) (registry.Source, *registrytest.Registry)) {
	s, r := newSource(t)
	ctx := t.Context()

	repos, err := s.Catalog(ctx)
	wantRepos := []string{registrytest.RepoHelm, registrytest.RepoEmpty, registrytest.RepoUntagged, registrytest.RepoSingle, registrytest.RepoIndex, registrytest.RepoAllMissing, registrytest.RepoDangling, registrytest.RepoSomeMissing, registrytest.RepoNested, registrytest.RepoPaged, registrytest.RepoUndated}
	if err != nil || !slices.Equal(repos, wantRepos) {
		t.Errorf("Catalog = %v, %v; want %v", repos, err, wantRepos)
	}

	for repo, tags := range map[string][]string{
		registrytest.RepoIndex:  {registrytest.TagVersion, registrytest.TagLatest, r.SignatureTag},
		registrytest.RepoSingle: {registrytest.TagSingle},
		registrytest.RepoHelm:   {registrytest.TagHelm},
		registrytest.RepoNested: {registrytest.TagNested},
		registrytest.RepoPaged:  registrytest.PagedTags,
		registrytest.RepoUndated: {
			registrytest.TagLabelled, registrytest.TagAnnotated, registrytest.TagIndexAnnotated,
			registrytest.TagBadLabel, registrytest.TagDated,
		},

		registrytest.RepoSomeMissing: {registrytest.TagVersion},
		registrytest.RepoAllMissing:  {registrytest.TagVersion},
		// A tag whose manifest is gone is still listed.
		registrytest.RepoDangling: {registrytest.TagVersion, registrytest.TagLatest},
		// Repositories with no tags left are empty, not unknown.
		registrytest.RepoEmpty:    {},
		registrytest.RepoUntagged: {},
	} {
		got, err := s.Tags(ctx, repo)
		if err != nil || !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(tags))) {
			t.Errorf("Tags(%s) = %v, %v; want %v", repo, got, err, tags)
		}
	}

	// The attestation child of the index is left out.
	var platforms []registry.Platform
	for _, img := range r.Platforms {
		platforms = append(platforms, want(t, img))
	}
	if v7 := platforms[2]; v7.Arch != "arm" || v7.Variant != "v7" {
		t.Fatalf("third platform = %+v, want linux/arm/v7", v7)
	}
	single := want(t, r.Single)
	index := registry.Manifest{Digest: digest(t, r.Index), MediaType: string(types.OCIImageIndex), Kind: registry.KindIndex, Platforms: platforms}
	image := registry.Manifest{Digest: single.Digest, MediaType: string(types.OCIManifestSchema1), Kind: registry.KindImage, Size: single.Size, Platforms: []registry.Platform{single}}
	artifact := func(img v1.Image, kind registry.Kind) registry.Manifest {
		p := want(t, img)
		return registry.Manifest{Digest: p.Digest, MediaType: string(types.OCIManifestSchema1), Kind: kind, Size: p.Size}
	}
	signature := artifact(r.Signature, registry.KindSignature)
	// A platform the registry lacks keeps what the index says, in its place.
	missing := func(p registry.Platform) registry.Platform {
		return registry.Platform{OS: p.OS, Arch: p.Arch, Variant: p.Variant, Digest: p.Digest, MediaType: p.MediaType, Missing: true}
	}
	someMissing, allMissing := index, index
	someMissing.Platforms = []registry.Platform{platforms[0], missing(platforms[1]), platforms[2]}
	allMissing.Platforms = []registry.Platform{missing(platforms[0]), missing(platforms[1]), missing(platforms[2])}
	for _, tc := range []struct {
		name, repo, ref string
		want            registry.Manifest
	}{
		{"index by tag", registrytest.RepoIndex, registrytest.TagVersion, index},
		{"index by alias", registrytest.RepoIndex, registrytest.TagLatest, index},
		{"index by digest", registrytest.RepoIndex, index.Digest, index},
		{"platform by digest", registrytest.RepoIndex, platforms[1].Digest, registry.Manifest{
			Digest: platforms[1].Digest, MediaType: string(types.OCIManifestSchema1), Kind: registry.KindImage, Size: platforms[1].Size, Platforms: platforms[1:2],
		}},
		{"single by tag", registrytest.RepoSingle, registrytest.TagSingle, image},
		{"single by digest", registrytest.RepoSingle, single.Digest, image},
		{"nested", registrytest.RepoNested, registrytest.TagNested, image},
		{"next to a dangling tag", registrytest.RepoDangling, registrytest.TagVersion, image},
		{"signature by tag", registrytest.RepoIndex, r.SignatureTag, signature},
		{"signature by digest", registrytest.RepoIndex, signature.Digest, signature},
		{"attestation by digest", registrytest.RepoIndex, digest(t, r.Attestation), artifact(r.Attestation, registry.KindSignature)},
		{"helm", registrytest.RepoHelm, registrytest.TagHelm, artifact(r.Helm, registry.KindHelm)},
		{"index with a platform missing", registrytest.RepoSomeMissing, registrytest.TagVersion, someMissing},
		{"index with every platform missing", registrytest.RepoAllMissing, registrytest.TagVersion, allMissing},
	} {
		// Before Resolve, so a Cache asks its source rather than its lookup.
		if !strings.HasPrefix(tc.ref, "sha256:") {
			if d, err := s.Digest(ctx, tc.repo, tc.ref); err != nil || d != tc.want.Digest {
				t.Errorf("%s: Digest(%s, %s) = %s, %v; want %s", tc.name, tc.repo, tc.ref, d, err, tc.want.Digest)
			}
		}
		m, err := s.Resolve(ctx, tc.repo, tc.ref)
		if err != nil || !manifestEqual(m, tc.want) {
			t.Errorf("%s: Resolve(%s, %s) = %+v, %v\nwant %+v", tc.name, tc.repo, tc.ref, m, err, tc.want)
		}
	}

	for _, p := range platforms {
		cfg, err := s.Config(ctx, registrytest.RepoIndex, p.Config)
		if err != nil || !cfg.Created.Equal(registrytest.PlatformsCreated) || !maps.Equal(cfg.Labels, registrytest.PlatformsLabels) {
			t.Errorf("Config(%s) = %+v, %v", p.Config, cfg, err)
		}
	}
	cfg, err := s.Config(ctx, registrytest.RepoSingle, single.Config)
	if err != nil || !cfg.Created.Equal(registrytest.SingleCreated) || len(cfg.Labels) != 0 {
		t.Errorf("Config(%s) = %+v, %v", single.Config, cfg, err)
	}

	// Without a created date in the config, the label dates the image, then
	// the manifest's annotation, then the index's. The config's date wins.
	for _, tc := range []struct {
		tag  string
		want time.Time
	}{
		{registrytest.TagLabelled, registrytest.LabelCreated},
		{registrytest.TagAnnotated, registrytest.AnnotationCreated},
		{registrytest.TagIndexAnnotated, registrytest.AnnotationCreated},
		{registrytest.TagBadLabel, time.Time{}},
		{registrytest.TagDated, registrytest.SingleCreated},
	} {
		m, err := s.Resolve(ctx, registrytest.RepoUndated, tc.tag)
		if err != nil || len(m.Platforms) != 1 {
			t.Fatalf("Resolve(%s) = %+v, %v", tc.tag, m, err)
		}
		p := m.Platforms[0]
		cfg, err := s.Config(ctx, registrytest.RepoUndated, p.Config)
		if err != nil {
			t.Fatalf("Config(%s) = %v", tc.tag, err)
		}
		if got := cmp.Or(cfg.Created, p.Created); !got.Equal(tc.want) {
			t.Errorf("%s: created = %v (config %v, platform %v), want %v", tc.tag, got, cfg.Created, p.Created, tc.want)
		}
	}

	// Only storage mode knows when a tag changed; TestStorageTagTime checks
	// the times.
	_, err = s.TagTime(ctx, registrytest.RepoSingle, registrytest.TagSingle)
	if r.StoragePath == "" && !errors.Is(err, errors.ErrUnsupported) || r.StoragePath != "" && err != nil {
		t.Errorf("TagTime error = %v", err)
	}
	// Likewise disk usage; TestStorageDiskUsage checks the sums.
	_, err = s.DiskUsage(ctx, "")
	if r.StoragePath == "" && !errors.Is(err, errors.ErrUnsupported) || r.StoragePath != "" && err != nil {
		t.Errorf("DiskUsage error = %v", err)
	}

	// The API answers an unknown repository's tags as it does an untagged
	// one's, so only storage can tell it isn't there.
	tags, err := s.Tags(ctx, "nope/missing")
	if r.StoragePath == "" && (err != nil || len(tags) != 0) || r.StoragePath != "" && !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("Tags of an unknown repo = %v, %v", tags, err)
	}

	zero := "sha256:" + strings.Repeat("0", 64)
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"tags of invalid name", func() error { _, err := s.Tags(ctx, "../library/nginx"); return err }},
		{"unknown tag", func() error { _, err := s.Resolve(ctx, registrytest.RepoIndex, "0.0.0"); return err }},
		{"resolve in unknown repo", func() error { _, err := s.Resolve(ctx, "nope/missing", registrytest.TagLatest); return err }},
		{"resolve in invalid name", func() error { _, err := s.Resolve(ctx, "Not A Repo", registrytest.TagLatest); return err }},
		{"unknown digest", func() error { _, err := s.Resolve(ctx, registrytest.RepoIndex, zero); return err }},
		{"digest from another repo", func() error { _, err := s.Resolve(ctx, registrytest.RepoSingle, index.Digest); return err }},
		{"tag whose manifest is gone", func() error { _, err := s.Resolve(ctx, registrytest.RepoDangling, registrytest.TagLatest); return err }},
		{"digest of a tag whose manifest is gone", func() error { _, err := s.Digest(ctx, registrytest.RepoDangling, registrytest.TagLatest); return err }},
		{"digest of an unknown tag", func() error { _, err := s.Digest(ctx, registrytest.RepoIndex, "0.0.0"); return err }},
		{"digest of an invalid tag", func() error { _, err := s.Digest(ctx, registrytest.RepoIndex, "../1.36"); return err }},
		{"missing platform by digest", func() error { _, err := s.Resolve(ctx, registrytest.RepoSomeMissing, platforms[1].Digest); return err }},
		{"malformed config digest", func() error { _, err := s.Config(ctx, registrytest.RepoIndex, "sha256:00"); return err }},
		{"unknown config digest", func() error { _, err := s.Config(ctx, registrytest.RepoIndex, zero); return err }},
	} {
		if err := tc.call(); !errors.Is(err, registry.ErrNotFound) {
			t.Errorf("%s: got %v, want ErrNotFound", tc.name, err)
		}
	}
}

// manifestEqual treats nil and empty Platforms alike.
func manifestEqual(a, b registry.Manifest) bool {
	return a.Digest == b.Digest && a.MediaType == b.MediaType && a.Kind == b.Kind && a.Size == b.Size && slices.Equal(a.Platforms, b.Platforms)
}
