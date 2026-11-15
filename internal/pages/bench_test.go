package pages

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

// The benchmarks below serve the pages whose cost grows with the registry
// through the whole UI, as main wires it, gzipped. Each warms a fresh cache
// with one visit and then measures repeat visits, over a generated registry
// in storage mode, where every sort is offered. Besides time and allocations
// they report the gzipped size of the page, and calls/op: how many calls per
// visit got past the cache to the registry. A page that reads more than the
// cache holds refetches all of it on every visit. `task bench` runs them.

// BenchmarkCatalog serves the catalog in each view, sort and filter. The
// default view and name sorts list bare names; the created, updated and
// tag-count sorts and the type filter read every repository first.
func BenchmarkCatalog(b *testing.B) {
	for _, repos := range []int{1000, 2000, 5000, 10000} {
		b.Run(fmt.Sprintf("repos=%d", repos), func(b *testing.B) {
			path := registrytest.NewLargeStorage(b, repos, 1).StoragePath
			for _, q := range []string{
				"view=compact", "view=comfortable", "view=detailed",
				"sort=name-desc", "sort=created", "sort=updated", "sort=tags",
				"q=app1", "type=image",
			} {
				b.Run(q, func(b *testing.B) { benchPage(b, path, "/?"+q) })
			}
		})
	}
}

// BenchmarkRepo serves a repository's overview and its tags tab in each
// sort and filter. Every visit sorts all its tags by version first.
func BenchmarkRepo(b *testing.B) {
	repo := "/" + registrytest.LargeRepo(0)
	for _, tags := range []int{1000, 5000} {
		b.Run(fmt.Sprintf("tags=%d", tags), func(b *testing.B) {
			path := registrytest.NewLargeStorage(b, 1, tags).StoragePath
			for _, q := range []string{
				"tab=overview", "tab=tags",
				"sort=name", "sort=created", "sort=updated", "sort=size",
				"q=1.4", "type=image",
			} {
				if !strings.HasPrefix(q, "tab=") {
					q = "tab=tags&" + q
				}
				b.Run(q, func(b *testing.B) { benchPage(b, path, repo+"?"+q) })
			}
		})
	}
}

// BenchmarkTag serves the page of a multi-arch index.
func BenchmarkTag(b *testing.B) {
	path := registrytest.NewLargeStorage(b, 1, 1).StoragePath
	benchPage(b, path, "/"+registrytest.LargeRepo(0)+":"+registrytest.LargeTag(0))
}

// benchPage measures visits to target in the UI over the storage at path.
func benchPage(b *testing.B, path, target string) {
	s, h := newGzipUI(b)
	st, err := registry.NewStorage(path)
	if err != nil {
		b.Fatal(err)
	}
	src := &counted{Source: st}
	s.Src, s.Storage = registry.NewCache(src), true

	b.ReportAllocs()
	rec := serve(h, http.MethodGet, target, "", false)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "took too long") {
		b.Fatalf("GET %s: %d\n%s", target, rec.Code, rec.Body)
	}
	src.n.Store(0)
	var size int
	for b.Loop() {
		size = serve(h, http.MethodGet, target, "gzip", false).Body.Len()
	}
	b.ReportMetric(float64(src.n.Load())/float64(b.N), "calls/op")
	b.ReportMetric(float64(size), "bytes/response")
}

// counted is a Source that counts the calls reaching it. The cache can't
// tell it's storage, so keeps lookups by digest for good instead of 30 s,
// which is longer than a benchmark runs anyway.
type counted struct {
	registry.Source
	n atomic.Int64
}

func (c *counted) Catalog(ctx context.Context) ([]string, error) {
	c.n.Add(1)
	return c.Source.Catalog(ctx)
}

func (c *counted) Tags(ctx context.Context, repo string) ([]string, error) {
	c.n.Add(1)
	return c.Source.Tags(ctx, repo)
}

func (c *counted) Resolve(ctx context.Context, repo, ref string) (registry.Manifest, error) {
	c.n.Add(1)
	return c.Source.Resolve(ctx, repo, ref)
}

func (c *counted) Digest(ctx context.Context, repo, tag string) (string, error) {
	c.n.Add(1)
	return c.Source.Digest(ctx, repo, tag)
}

func (c *counted) Config(ctx context.Context, repo, digest string) (registry.ImageConfig, error) {
	c.n.Add(1)
	return c.Source.Config(ctx, repo, digest)
}

func (c *counted) TagTime(ctx context.Context, repo, tag string) (time.Time, error) {
	c.n.Add(1)
	return c.Source.TagTime(ctx, repo, tag)
}

func (c *counted) DiskUsage(ctx context.Context, repo string) (int64, error) {
	c.n.Add(1)
	return c.Source.DiskUsage(ctx, repo)
}
