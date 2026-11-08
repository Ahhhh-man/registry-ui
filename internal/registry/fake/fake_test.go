package fake_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/fake"
)

func TestCatalog(t *testing.T) {
	repos, err := fake.New().Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"library/nginx", "library/postgres", "charts/podinfo", "myteam/web/frontend"} {
		if !slices.Contains(repos, want) {
			t.Errorf("catalog is missing %s", want)
		}
	}
	if !slices.IsSorted(repos) {
		t.Errorf("catalog isn't sorted: %v", repos)
	}
}

func TestNginx(t *testing.T) {
	ctx, s := t.Context(), fake.New()

	latest, err := s.Resolve(ctx, "library/nginx", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Kind != registry.KindIndex || len(latest.Platforms) != 6 {
		t.Errorf("latest: got kind %v with %d platforms, want an index with 6", latest.Kind, len(latest.Platforms))
	}
	for _, alias := range []string{"1.27.3", "mainline", "1.27"} {
		m, err := s.Resolve(ctx, "library/nginx", alias)
		if err != nil || m.Digest != latest.Digest {
			t.Errorf("%s should share latest's digest: %v %v", alias, m.Digest, err)
		}
	}

	byDigest, err := s.Resolve(ctx, "library/nginx", latest.Digest)
	if err != nil || byDigest.Digest != latest.Digest {
		t.Errorf("resolve by digest: %v %v", byDigest.Digest, err)
	}

	p := latest.Platforms[2]
	if p.OS != "linux" || p.Arch != "arm" || p.Variant != "v7" || p.Size == 0 {
		t.Errorf("platform 2 = %+v, want linux/arm/v7 with a size", p)
	}
	if one, err := s.Resolve(ctx, "library/nginx", p.Digest); err != nil || one.Kind != registry.KindImage {
		t.Errorf("platform manifest by digest: %+v %v", one, err)
	}

	cfg, err := s.Config(ctx, "library/nginx", p.Config)
	if err != nil || cfg.Created.IsZero() || cfg.Labels["org.opencontainers.image.description"] == "" {
		t.Errorf("config: %+v %v", cfg, err)
	}

	tags, _ := s.Tags(ctx, "library/nginx")
	sig := "sha256-" + strings.TrimPrefix(latest.Digest, "sha256:") + ".sig"
	if !slices.Contains(tags, sig) {
		t.Fatalf("tags are missing the signature %s", sig)
	}
	if m, _ := s.Resolve(ctx, "library/nginx", sig); m.Kind != registry.KindSignature {
		t.Errorf("signature kind = %v", m.Kind)
	}
}

func TestKinds(t *testing.T) {
	ctx, s := t.Context(), fake.New()
	for _, tc := range []struct {
		repo, tag string
		kind      registry.Kind
	}{
		{"charts/podinfo", "6.7.1", registry.KindHelm},
		{"myteam/web/frontend", "main", registry.KindImage},
	} {
		m, err := s.Resolve(ctx, tc.repo, tc.tag)
		if err != nil || m.Kind != tc.kind {
			t.Errorf("%s:%s: kind %v, err %v; want %v", tc.repo, tc.tag, m.Kind, err, tc.kind)
		}
	}
}

func TestNotFound(t *testing.T) {
	ctx, s := t.Context(), fake.New()
	_, err1 := s.Tags(ctx, "nope/missing")
	_, err2 := s.Resolve(ctx, "library/nginx", "0.0.0")
	_, err3 := s.Config(ctx, "library/nginx", "sha256:00")
	for _, err := range []error{err1, err2, err3} {
		if !errors.Is(err, registry.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	}
}

func TestUnauthorizedError(t *testing.T) {
	var err error = &registry.UnauthorizedError{Challenged: true}
	if !errors.Is(err, registry.ErrUnauthorized) {
		t.Error("UnauthorizedError should match ErrUnauthorized")
	}
}
