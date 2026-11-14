package registry

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// counter is a Source that counts upstream calls. Its answers name the
// identity that asked, so a leak between identities shows.
type counter struct {
	calls atomic.Int32
	gate  chan struct{} // when set, calls wait for it to close
	err   error
}

func (s *counter) call(ctx context.Context) (string, error) {
	s.calls.Add(1)
	if s.gate != nil {
		<-s.gate
	}
	if cr, ok := ctx.Value(credentialsKey{}).(credentials); ok {
		return cr.user, s.err
	}
	return "anonymous", s.err
}

func (s *counter) Catalog(ctx context.Context) ([]string, error) {
	who, err := s.call(ctx)
	return []string{who}, err
}

func (s *counter) Tags(ctx context.Context, _ string) ([]string, error) {
	who, err := s.call(ctx)
	return []string{who}, err
}

func (s *counter) Resolve(ctx context.Context, _, ref string) (Manifest, error) {
	_, err := s.call(ctx)
	return Manifest{Digest: ref, Platforms: []Platform{{OS: "linux"}}}, err
}

func (s *counter) Config(ctx context.Context, _, _ string) (ImageConfig, error) {
	who, err := s.call(ctx)
	return ImageConfig{Labels: map[string]string{"who": who}}, err
}

func (s *counter) TagTime(ctx context.Context, _, _ string) (time.Time, error) {
	_, err := s.call(ctx)
	return time.Unix(0, 0), err
}

func (s *counter) DiskUsage(ctx context.Context, _ string) (int64, error) {
	_, err := s.call(ctx)
	return 1, err
}

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func (s *counter) want(t *testing.T, n int32) {
	t.Helper()
	if got := s.calls.Load(); got != n {
		t.Fatalf("upstream calls = %d, want %d", got, n)
	}
}

// Every test runs in a synctest bubble: time is fake, and the test fails if
// the cache leaves a goroutine running.
func TestCacheTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		src := &counter{}
		c := NewCache(src)
		fetch := func() {
			_, _ = c.Catalog(ctx)
			_, _ = c.Tags(ctx, "repo")
			_, _ = c.Resolve(ctx, "repo", "latest")
			_, _ = c.Resolve(ctx, "repo", testDigest)
			_, _ = c.Config(ctx, "repo", testDigest)
			_, _ = c.TagTime(ctx, "repo", "latest")
			_, _ = c.DiskUsage(ctx, "")
		}

		fetch()
		src.want(t, 7)
		time.Sleep(cacheTTL - time.Second)
		fetch()
		src.want(t, 7)

		// Catalog, tags, the tag lookup, the tag time and disk usage expire;
		// digest lookups don't.
		time.Sleep(time.Second)
		fetch()
		src.want(t, 12)
		time.Sleep(365 * 24 * time.Hour)
		fetch()
		src.want(t, 17)
	})
}

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		src := &counter{}
		c := NewCache(src)
		c.max = 2
		get := func(d string) { _, _ = c.Config(ctx, "repo", d) }

		get("a")
		get("b")
		get("a") // a is now more recent than b
		get("c") // evicts b
		src.want(t, 3)
		get("a")
		get("c")
		src.want(t, 3)
		get("b")
		src.want(t, 4)
		if n := len(c.items); n != 2 {
			t.Errorf("cache holds %d entries, want 2", n)
		}
	})
}

// A catalog sort loads a tag list, a manifest and a config for every
// repository. If they don't all fit, the next visit starts over.
func TestCacheHoldsCatalogSort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		src := &counter{}
		c := NewCache(src)
		row := func(i int) {
			repo := "repo-" + strconv.Itoa(i)
			_, _ = c.Tags(ctx, repo)
			_, _ = c.Resolve(ctx, repo, "latest")
			_, _ = c.Config(ctx, repo, testDigest)
		}
		const repos = 1500 // with room to spare over the 1,000 of the memory budget
		for i := range repos {
			row(i)
		}
		src.want(t, 3*repos)
		row(0)
		src.want(t, 3*repos)
	})
}

func TestCacheScopesEntriesToIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &counter{}
		c := NewCache(src)
		ctxs := map[string]context.Context{
			"anonymous": t.Context(),
			"alice":     WithCredentials(t.Context(), "alice", "secret-1"),
			"bob":       WithCredentials(t.Context(), "bob", "secret-1"),
		}
		for range 2 {
			for who, ctx := range ctxs {
				got, err := c.Catalog(ctx)
				if err != nil || len(got) != 1 || got[0] != who {
					t.Fatalf("Catalog as %s = %v, %v", who, got, err)
				}
			}
		}
		src.want(t, 3)

		// A different password is a different identity.
		other := WithCredentials(t.Context(), "alice", "secret-2")
		_, _ = c.Catalog(other)
		src.want(t, 4)

		for k := range c.items {
			if strings.Contains(k, "secret") {
				t.Errorf("cache key %q holds a password", k)
			}
		}
	})
}

func TestCacheDoesNotCacheErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &counter{err: ErrUnavailable}
		c := NewCache(src)
		for range 2 {
			if _, err := c.Resolve(t.Context(), "repo", testDigest); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Resolve error = %v, want ErrUnavailable", err)
			}
		}
		src.want(t, 2)
	})
}

func TestCacheCollapsesConcurrentMisses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &counter{gate: make(chan struct{})}
		c := NewCache(src)
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				if tags, err := c.Tags(t.Context(), "repo"); err != nil || len(tags) != 1 {
					t.Errorf("Tags = %v, %v", tags, err)
				}
			})
		}
		synctest.Wait() // all ten are waiting on the one upstream call
		close(src.gate)
		wg.Wait()
		src.want(t, 1)
	})
}

func TestCacheReturnsCopies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := NewCache(&counter{})
		tags, _ := c.Tags(t.Context(), "repo")
		tags[0] = "changed"
		m, _ := c.Resolve(t.Context(), "repo", testDigest)
		m.Platforms[0].OS = "changed"
		cfg, _ := c.Config(t.Context(), "repo", testDigest)
		cfg.Labels["who"] = "changed"

		tags, _ = c.Tags(t.Context(), "repo")
		m, _ = c.Resolve(t.Context(), "repo", testDigest)
		cfg, _ = c.Config(t.Context(), "repo", testDigest)
		if tags[0] == "changed" || m.Platforms[0].OS == "changed" || cfg.Labels["who"] == "changed" {
			t.Error("a caller's change reached the cache")
		}
	})
}

// BenchmarkCacheHit looks up a manifest the cache holds, as a page does for
// each tag or repository it shows once the cache is warm. Signed in, the key
// carries a hash of the credentials.
func BenchmarkCacheHit(b *testing.B) {
	hit := func(b *testing.B, ctx context.Context) {
		src := &counter{}
		c := NewCache(src)
		_, _ = c.Resolve(ctx, "repo", "tag")
		b.ReportAllocs()
		for b.Loop() {
			_, _ = c.Resolve(ctx, "repo", "tag")
		}
		if n := src.calls.Load(); n != 1 {
			b.Fatalf("upstream calls = %d, want 1", n)
		}
	}
	b.Run("anonymous", func(b *testing.B) { hit(b, context.Background()) })
	b.Run("signed-in", func(b *testing.B) { hit(b, WithCredentials(context.Background(), "alice", "secret")) })
}
