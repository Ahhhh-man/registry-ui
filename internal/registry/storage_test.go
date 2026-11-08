package registry_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

func newStorage(t *testing.T) (*registry.Storage, *registrytest.Registry) {
	t.Helper()
	r := registrytest.NewStorage(t)
	s, err := registry.NewStorage(r.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

// removeBlob deletes a blob from the storage, as if a pull-through cache had
// never fetched it.
func removeBlob(t *testing.T, r *registrytest.Registry, digest string) {
	t.Helper()
	hex := strings.TrimPrefix(digest, "sha256:")
	if err := os.Remove(filepath.Join(r.StoragePath, "docker", "registry", "v2", "blobs", "sha256", hex[:2], hex, "data")); err != nil {
		t.Fatal(err)
	}
}

func TestStorageUncached(t *testing.T) {
	s, r := newStorage(t)
	ctx := t.Context()

	// An index child that was never pulled is left out.
	arm64 := digest(t, r.Platforms[1])
	removeBlob(t, r, arm64)
	m, err := s.Resolve(ctx, registrytest.RepoIndex, registrytest.TagVersion)
	if err != nil || len(m.Platforms) != 2 || slices.ContainsFunc(m.Platforms, func(p registry.Platform) bool { return p.Digest == arm64 }) {
		t.Errorf("index without arm64 = %+v, %v", m, err)
	}
	if _, err := s.Resolve(ctx, registrytest.RepoIndex, arm64); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("arm64 by digest: got %v, want ErrNotFound", err)
	}

	// A manifest without its config still shows, without OS and arch.
	p := want(t, r.Single)
	removeBlob(t, r, p.Config)
	m, err = s.Resolve(ctx, registrytest.RepoSingle, registrytest.TagSingle)
	if err != nil || len(m.Platforms) != 1 || m.Platforms[0].Digest != p.Digest || m.Platforms[0].OS != "" || m.Size != p.Size {
		t.Errorf("single without config = %+v, %v", m, err)
	}
	if _, err := s.Config(ctx, registrytest.RepoSingle, p.Config); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("missing config: got %v, want ErrNotFound", err)
	}

	// A tag whose manifest blob is missing isn't there.
	removeBlob(t, r, digest(t, r.Helm))
	if tags, err := s.Tags(ctx, registrytest.RepoHelm); err != nil || len(tags) != 0 {
		t.Errorf("Tags with a missing blob = %v, %v; want none", tags, err)
	}
	if _, err := s.Resolve(ctx, registrytest.RepoHelm, registrytest.TagHelm); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("tag with a missing blob: got %v, want ErrNotFound", err)
	}
}

func TestStorageNotFound(t *testing.T) {
	s, r := newStorage(t)
	ctx := t.Context()

	// A tag link that points outside the blob store is ignored.
	bad := filepath.Join(r.StoragePath, "docker", "registry", "v2", "repositories", registrytest.RepoSingle, "_manifests", "tags", "evil", "current", "link")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("sha256:../../../../../etc/passwd"), 0o644); err != nil {
		t.Fatal(err)
	}
	if tags, err := s.Tags(ctx, registrytest.RepoSingle); err != nil || !slices.Equal(tags, []string{registrytest.TagSingle}) {
		t.Errorf("Tags = %v, %v; want only %s", tags, err, registrytest.TagSingle)
	}

	_, err1 := s.Tags(ctx, "library") // a namespace, not a repository
	_, err2 := s.Resolve(ctx, registrytest.RepoIndex, "../../busybox/_manifests/tags/1.36")
	_, err3 := s.Resolve(ctx, registrytest.RepoSingle, "evil")
	for i, err := range []error{err1, err2, err3} {
		if !errors.Is(err, registry.ErrNotFound) {
			t.Errorf("%d: got %v, want ErrNotFound", i+1, err)
		}
	}
}

func TestNewStorage(t *testing.T) {
	// An empty folder is a registry nobody has pushed to yet.
	s, err := registry.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if repos, err := s.Catalog(t.Context()); err != nil || len(repos) != 0 {
		t.Errorf("empty Catalog = %v, %v", repos, err)
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{other, filepath.Join(other, "missing")} {
		if _, err := registry.NewStorage(dir); err == nil {
			t.Errorf("NewStorage(%q) should fail", dir)
		}
	}
}
