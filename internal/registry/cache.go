package registry

import (
	"container/list"
	"context"
	"crypto/sha256"
	"maps"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// cacheSize is how many entries the cache holds before evicting the
	// least recently used.
	cacheSize = 5000
	// cacheTTL is how long the catalog, tag lists, tag lookups, tag times
	// and disk usage are trusted. Anything addressed by digest can't change, so it never expires.
	cacheTTL = 30 * time.Second
)

// Cache is a Source that remembers another Source's answers in memory.
// Entries are scoped to the identity in the context, so one user's results
// are never served to another user or to anonymous visitors. Errors are not
// cached. It does no work between calls: expired entries are refetched when
// next asked for.
type Cache struct {
	src   Source
	group singleflight.Group
	// partial is set for storage mode, where an index's platforms stop being
	// missing as they're pulled, so even a lookup by digest must expire.
	partial bool

	mu    sync.Mutex
	max   int
	order *list.List // of *cacheEntry, most recently used first
	items map[string]*list.Element
}

type cacheEntry struct {
	key     string
	val     any
	expires time.Time // zero: until evicted
}

var _ Source = (*Cache)(nil)

// NewCache returns a Cache in front of src.
func NewCache(src Source) *Cache {
	_, partial := src.(*Storage)
	return &Cache{src: src, partial: partial, max: cacheSize, order: list.New(), items: map[string]*list.Element{}}
}

// Catalog implements Source.
func (c *Cache) Catalog(ctx context.Context) ([]string, error) {
	v, err := cached(ctx, c, cacheTTL, func(ctx context.Context) ([]string, error) { return c.src.Catalog(ctx) }, "catalog")
	return slices.Clone(v), err
}

// Tags implements Source.
func (c *Cache) Tags(ctx context.Context, repo string) ([]string, error) {
	v, err := cached(ctx, c, cacheTTL, func(ctx context.Context) ([]string, error) { return c.src.Tags(ctx, repo) }, "tags", repo)
	return slices.Clone(v), err
}

// Resolve implements Source.
func (c *Cache) Resolve(ctx context.Context, repo, ref string) (Manifest, error) {
	ttl := cacheTTL
	if digestRef.MatchString(ref) && !c.partial {
		ttl = 0
	}
	m, err := cached(ctx, c, ttl, func(ctx context.Context) (Manifest, error) { return c.src.Resolve(ctx, repo, ref) }, "manifest", repo, ref)
	m.Platforms = slices.Clone(m.Platforms)
	return m, err
}

// Config implements Source.
func (c *Cache) Config(ctx context.Context, repo, digest string) (ImageConfig, error) {
	cfg, err := cached(ctx, c, 0, func(ctx context.Context) (ImageConfig, error) { return c.src.Config(ctx, repo, digest) }, "config", repo, digest)
	cfg.Labels = maps.Clone(cfg.Labels)
	return cfg, err
}

// TagTime implements Source.
func (c *Cache) TagTime(ctx context.Context, repo, tag string) (time.Time, error) {
	return cached(ctx, c, cacheTTL, func(ctx context.Context) (time.Time, error) { return c.src.TagTime(ctx, repo, tag) }, "tag time", repo, tag)
}

// DiskUsage implements Source. In storage mode it walks the blob store, so
// it mustn't run on every page view.
func (c *Cache) DiskUsage(ctx context.Context, repo string) (int64, error) {
	return cached(ctx, c, cacheTTL, func(ctx context.Context) (int64, error) { return c.src.DiskUsage(ctx, repo) }, "disk usage", repo)
}

// cached returns the entry for key in ctx's identity, or calls fetch once
// for all concurrent callers and keeps a successful result for ttl (0:
// until evicted). Callers must copy slices and maps before handing them out.
func cached[T any](ctx context.Context, c *Cache, ttl time.Duration, fetch func(context.Context) (T, error), key ...string) (T, error) {
	k := identity(ctx)
	for _, s := range key {
		k += "\x00" + s
	}
	if v, ok := c.get(k); ok {
		return v.(T), nil
	}
	v, err, _ := c.group.Do(k, func() (any, error) {
		// Shared by every waiting caller, so one giving up mustn't fail the
		// rest. Each upstream request has its own timeout.
		v, err := fetch(context.WithoutCancel(ctx))
		if err == nil {
			c.put(k, v, ttl)
		}
		return v, err
	})
	t, _ := v.(T)
	return t, err
}

// identity names who ctx signs in as: "a" for anonymous, or "u" and a hash
// of the credentials, so keys never hold a password.
func identity(ctx context.Context) string {
	cr, ok := ctx.Value(credentialsKey{}).(credentials)
	if !ok {
		return "a"
	}
	sum := sha256.Sum256([]byte(cr.user + "\x00" + cr.pass))
	return "u" + string(sum[:])
}

func (c *Cache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*cacheEntry)
	if !e.expires.IsZero() && !time.Now().Before(e.expires) {
		c.order.Remove(el)
		delete(c.items, key)
		return nil, false
	}
	c.order.MoveToFront(el)
	return e.val, true
}

func (c *Cache) put(key string, val any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := &cacheEntry{key: key, val: val}
	if ttl > 0 {
		e.expires = time.Now().Add(ttl)
	}
	if el, ok := c.items[key]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(e)
	if c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*cacheEntry).key)
	}
}
