package registry_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

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
	if err := os.Remove(blobPath(r, digest)); err != nil {
		t.Fatal(err)
	}
}

func blobPath(r *registrytest.Registry, digest string) string {
	hex := strings.TrimPrefix(digest, "sha256:")
	return filepath.Join(r.StoragePath, "docker", "registry", "v2", "blobs", "sha256", hex[:2], hex, "data")
}

// In storage mode an index lists only the platforms on disk, so the cache
// must pick up a platform pulled after the index was first looked up, even
// by digest.
func TestCacheRefreshesStorageIndex(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, r := newStorage(t)
		c := registry.NewCache(s)
		idx, arm64 := digest(t, r.Index), digest(t, r.Platforms[1])
		platforms := func() int {
			t.Helper()
			m, err := c.Resolve(t.Context(), registrytest.RepoIndex, idx)
			if err != nil {
				t.Fatal(err)
			}
			return len(m.Platforms)
		}

		blob := blobPath(r, arm64)
		if err := os.Rename(blob, blob+".pulling"); err != nil {
			t.Fatal(err)
		}
		if n := platforms(); n != 2 {
			t.Fatalf("before arm64 is pulled: %d platforms, want 2", n)
		}
		if err := os.Rename(blob+".pulling", blob); err != nil {
			t.Fatal(err)
		}
		if n := platforms(); n != 2 {
			t.Errorf("right after the pull: %d platforms, want the cached 2", n)
		}
		time.Sleep(31 * time.Second)
		if n := platforms(); n != 3 {
			t.Errorf("after the cache expires: %d platforms, want 3", n)
		}
	})
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
