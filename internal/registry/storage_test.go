package registry_test

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

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

// In storage mode an index marks the platforms not on disk missing, so the
// cache must pick up a platform pulled after the index was first looked up,
// even by digest.
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
			return len(slices.DeleteFunc(m.Platforms, func(p registry.Platform) bool { return p.Missing }))
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

func TestStorageTagTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, r := newStorage(t)
		c := registry.NewCache(s)
		ctx := t.Context()
		tagTime := func(src registry.Source) time.Time {
			t.Helper()
			got, err := src.TagTime(ctx, registrytest.RepoSingle, registrytest.TagSingle)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}

		pushed := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
		r.SetTagTime(t, registrytest.RepoSingle, registrytest.TagSingle, pushed)
		if got := tagTime(s); !got.Equal(pushed) {
			t.Errorf("TagTime = %v, want %v", got, pushed)
		}
		if got := tagTime(c); !got.Equal(pushed) {
			t.Errorf("cached TagTime = %v, want %v", got, pushed)
		}

		// A pull through a cache rewrites the link; the cache catches up
		// once its entry expires.
		pulled := pushed.Add(time.Hour)
		r.SetTagTime(t, registrytest.RepoSingle, registrytest.TagSingle, pulled)
		if got := tagTime(c); !got.Equal(pushed) {
			t.Errorf("right after the pull: %v, want the cached %v", got, pushed)
		}
		time.Sleep(31 * time.Second)
		if got := tagTime(c); !got.Equal(pulled) {
			t.Errorf("after the cache expires: %v, want %v", got, pulled)
		}

		for _, ref := range [][2]string{
			{registrytest.RepoSingle, "0.0.0"},
			{registrytest.RepoSingle, "../1.36"},
			{"nope/missing", registrytest.TagSingle},
			{"Not A Repo", registrytest.TagSingle},
		} {
			if _, err := s.TagTime(ctx, ref[0], ref[1]); !errors.Is(err, registry.ErrNotFound) {
				t.Errorf("TagTime(%s, %s): got %v, want ErrNotFound", ref[0], ref[1], err)
			}
		}
	})
}

// blobSizes adds the size of each of img's blobs to sizes, by digest.
func blobSizes(t *testing.T, sizes map[string]int64, img v1.Image) {
	t.Helper()
	m, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range append(m.Layers, m.Config) {
		sizes[d.Digest.String()] = d.Size
	}
	raw, err := img.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	sizes[digest(t, img)] = int64(len(raw))
}

func sum(sizes map[string]int64) (n int64) {
	for _, s := range sizes {
		n += s
	}
	return n
}

func TestStorageDiskUsage(t *testing.T) {
	s, r := newStorage(t)
	ctx := t.Context()
	usage := func(repo string) int64 {
		t.Helper()
		n, err := s.DiskUsage(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	single := map[string]int64{}
	blobSizes(t, single, r.Single)
	all := maps.Clone(single)
	for _, img := range append(r.Platforms, r.Attestation, r.Signature, r.Helm) {
		blobSizes(t, all, img)
	}
	raw, err := r.Index.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	all[digest(t, r.Index)] = int64(len(raw))

	// Single's blobs are shared by three repositories, and test/paged links
	// them under ten tags: each repository counts them once, and so does the
	// registry.
	for _, repo := range []string{registrytest.RepoSingle, registrytest.RepoNested, registrytest.RepoPaged} {
		if got := usage(repo); got != sum(single) {
			t.Errorf("DiskUsage(%s) = %d, want %d", repo, got, sum(single))
		}
	}
	if got := usage(""); got != sum(all) {
		t.Errorf("DiskUsage of the registry = %d, want %d", got, sum(all))
	}

	// A pull-through cache may hold a link before (or without) its blob.
	layers, err := r.Single.Layers()
	if err != nil {
		t.Fatal(err)
	}
	gone := digest(t, layers[0])
	removeBlob(t, r, gone)
	if got, want := usage(registrytest.RepoSingle), sum(single)-single[gone]; got != want {
		t.Errorf("with a blob missing: DiskUsage = %d, want %d", got, want)
	}
	if got, want := usage(""), sum(all)-single[gone]; got != want {
		t.Errorf("with a blob missing: registry DiskUsage = %d, want %d", got, want)
	}

	for _, repo := range []string{"nope/missing", "Not A Repo", "../library"} {
		if _, err := s.DiskUsage(ctx, repo); !errors.Is(err, registry.ErrNotFound) {
			t.Errorf("DiskUsage(%s): got %v, want ErrNotFound", repo, err)
		}
	}

	// A cancelled request stops the walk.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, repo := range []string{"", registrytest.RepoSingle} {
		if _, err := s.DiskUsage(cancelled, repo); !errors.Is(err, context.Canceled) {
			t.Errorf("DiskUsage(%q) after cancel: got %v, want context.Canceled", repo, err)
		}
	}
}

func TestStorageUncached(t *testing.T) {
	s, r := newStorage(t)
	ctx := t.Context()

	// An index child whose blob was never stored is missing.
	arm64 := digest(t, r.Platforms[1])
	removeBlob(t, r, arm64)
	m, err := s.Resolve(ctx, registrytest.RepoIndex, registrytest.TagVersion)
	if err != nil || len(m.Platforms) != 3 || m.Platforms[1].Digest != arm64 || !m.Platforms[1].Missing || m.Platforms[0].Missing {
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
