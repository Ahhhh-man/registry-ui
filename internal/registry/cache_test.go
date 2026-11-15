package registry

import (
	"context"
	"errors"
	"reflect"
	"slices"
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

func (s *counter) Digest(ctx context.Context, _, tag string) (string, error) {
	_, err := s.call(ctx)
	return tag, err
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
// A tag's digest comes from its lookup while that's fresh, and is cached
// on its own otherwise.
func TestCacheDigest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		src := &counter{}
		c := NewCache(src)
		if _, err := c.Resolve(ctx, "r", "a"); err != nil {
			t.Fatal(err)
		}
		if d, err := c.Digest(ctx, "r", "a"); err != nil || d != "a" {
			t.Fatalf("Digest = %s, %v", d, err)
		}
		src.want(t, 1)
		for range 2 {
			if d, err := c.Digest(ctx, "r", "b"); err != nil || d != "b" {
				t.Fatalf("Digest = %s, %v", d, err)
			}
		}
		src.want(t, 2)
		time.Sleep(cacheTTL)
		_, _ = c.Digest(ctx, "r", "a")
		src.want(t, 3)
	})
}

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

func TestCacheEvictsUnvisited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		src := &counter{}
		c := NewCache(src)
		c.max = 2 * configSize(ctx, "a")
		get := func(d string) { _, _ = c.Config(ctx, "repo", d) }

		get("a")
		get("b")
		get("a") // a has been visited since it was added, b hasn't
		get("c") // evicts b
		src.want(t, 3)
		get("a")
		get("c")
		src.want(t, 3)
		get("b") // just evicted, so not kept the first time it's back
		get("a")
		get("c")
		src.want(t, 4)
		get("b") // but kept the second
		get("b")
		src.want(t, 5)
		if n := len(c.items); n != 2 {
			t.Errorf("cache holds %d entries, want 2", n)
		}
	})
}

// configSize is the size of the entry counter's Config answer for digest.
func configSize(ctx context.Context, digest string) int {
	return entrySize(entryKey(ctx, "config", "repo", digest), ImageConfig{Labels: map[string]string{"who": "anonymous"}})
}

// tagLists is a counter whose repositories have as many tags as their name
// says.
type tagLists struct{ counter }

func (s *tagLists) Tags(ctx context.Context, repo string) ([]string, error) {
	_, err := s.call(ctx)
	n, _ := strconv.Atoi(repo)
	return slices.Repeat([]string{"v1.2.3"}, n), err
}

// The cache stays within its byte limit whatever mix of entries it holds,
// and doesn't keep an entry bigger than a tenth of cacheSize.
func TestCacheBoundedBySize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		src := &tagLists{}
		c := NewCache(src)
		for i := range 3000 {
			if i%10 == 0 {
				_, _ = c.Tags(ctx, strconv.Itoa(i*3)) // up to 9,000 tags, about 200 kB
			}
			_, _ = c.TagTime(ctx, "repo", strconv.Itoa(i))
			sum := 0
			for _, el := range c.items {
				sum += el.Value.(*cacheEntry).size
			}
			if sum != c.used || c.used > c.max {
				t.Fatalf("after %d: entries take %d bytes, used says %d, limit %d", i, sum, c.used, c.max)
			}
		}

		huge := strconv.Itoa(cacheSize/10/22 + 100) // 22 bytes a tag: past a tenth
		calls := src.calls.Load()
		_, _ = c.Tags(ctx, huge)
		_, _ = c.Tags(ctx, huge)
		src.want(t, calls+2)
		if _, ok := c.items[entryKey(ctx, "tags", huge)]; ok {
			t.Error("cache kept an entry bigger than a tenth of its limit")
		}
	})
}

// Every answer a Source gives has a size estimate, so a new type or a new
// method can't be cached as if it took nothing.
func TestEntrySize(t *testing.T) {
	src := reflect.TypeFor[Source]()
	for i := range src.NumMethod() {
		m := src.Method(i)
		t.Run(m.Name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Error(r)
				}
			}()
			if n := entrySize("", reflect.Zero(m.Type.Out(0)).Interface()); n <= entryOverhead {
				t.Errorf("entrySize = %d, want more than the overhead %d", n, entryOverhead)
			}
		})
	}
}

// A catalog sort reads every repository in the same order on each visit.
// When that's more than the cache holds, most of a visit should still come
// from the cache, not each entry be evicted just before it's needed again.
func TestCacheSurvivesRepeatedScan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		src := &counter{}
		c := NewCache(src)
		c.max = 1000 * configSize(ctx, "100")
		const keys = 1200
		for pass := range 6 {
			before := src.calls.Load()
			for i := range keys {
				_, _ = c.Config(ctx, "repo", strconv.Itoa(i))
			}
			misses := src.calls.Load() - before
			t.Logf("pass %d fetched %d of %d", pass+1, misses, keys)
			if pass > 0 && misses > keys/2 {
				t.Errorf("pass %d fetched %d of %d entries, want at most half", pass+1, misses, keys)
			}
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
