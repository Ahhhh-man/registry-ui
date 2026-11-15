package registry

import (
	"container/list"
	"context"
	"crypto/sha256"
	"hash/maphash"
	"maps"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// cacheSize is how many entries the cache holds before evicting one. A
	// catalog sort or type filter needs three per repository (tag list,
	// manifest, config) and a repository's tag sort two per tag, for each
	// signed-in user. This fits one user's sort of
	// about 1,600 repositories, and keeps the cache within the 15 MB idle
	// memory budget: realistic entries cost 1–2 kB each.
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

	// Eviction is SIEVE (https://cachemon.github.io/SIEVE-website/): a hit
	// only marks its entry visited, and the hand walks from the oldest entry
	// towards the newest, clearing the mark on visited entries and evicting
	// the first one without it.
	mu    sync.Mutex
	max   int
	order *list.List    // of *cacheEntry, newest first
	hand  *list.Element // where the next eviction starts looking; nil: the oldest
	items map[string]*list.Element
	// gone holds hashes of the last max evicted keys, each mapped to its
	// slot in ring. A key that comes back while it's here is being read in a
	// cycle longer than the cache holds, such as a catalog sort, so it isn't
	// kept the first time: keeping it would evict what the cycle needs next.
	seed maphash.Seed
	gone map[uint64]int
	ring []uint64
	next int
}

type cacheEntry struct {
	key     string
	val     any
	expires time.Time // zero: until evicted
	visited bool
}

var _ Source = (*Cache)(nil)

// NewCache returns a Cache in front of src.
func NewCache(src Source) *Cache {
	_, partial := src.(*Storage)
	return &Cache{
		src: src, partial: partial, max: cacheSize, order: list.New(), items: map[string]*list.Element{},
		seed: maphash.MakeSeed(), gone: map[uint64]int{},
	}
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
	k := entryKey(ctx, key...)
	last, fresh := c.get(k)
	if fresh {
		return last.(T), nil
	}
	v, err, _ := c.group.Do(k, func() (any, error) {
		// Shared by every waiting caller, so one giving up mustn't fail the
		// rest. Each upstream request has its own timeout.
		v, err := fetch(context.WithValue(context.WithoutCancel(ctx), askedKey{}, asked{c, last}))
		if err == nil {
			c.put(k, v, ttl)
		}
		return v, err
	})
	t, _ := v.(T)
	return t, err
}

// asked is what the Cache tells the Source it asks, in the call's context,
// so the Source needn't fetch again what is addressed by digest.
type asked struct {
	cache *Cache
	// last is the answer to the same question that has just expired, if the
	// cache still holds it.
	last any
}

type askedKey struct{}

// keepConfig files cfg as the answer to Config, for a Source that read it
// on the way to answering something else.
func (c *Cache) keepConfig(ctx context.Context, repo, digest string, cfg ImageConfig) {
	c.put(entryKey(ctx, "config", repo, digest), cfg, 0)
}

// entryKey is where the answer to key is filed for ctx's identity.
func entryKey(ctx context.Context, key ...string) string {
	k := identity(ctx)
	for _, s := range key {
		k += "\x00" + s
	}
	return k
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

// get returns the entry for key and whether it can still be served. One that
// has expired stays until it is replaced or evicted.
func (c *Cache) get(key string) (val any, fresh bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*cacheEntry)
	if !e.expires.IsZero() && !time.Now().Before(e.expires) {
		return e.val, false
	}
	e.visited = true
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
		e.visited = true
		el.Value = e
		return
	}
	if c.order.Len() >= c.max {
		h := maphash.String(c.seed, key)
		if _, ok := c.gone[h]; ok {
			delete(c.gone, h)
			return
		}
		c.evict()
	}
	c.items[key] = c.order.PushFront(e)
}

// evict removes the first entry from the hand on that hasn't been visited
// since the hand last passed it, wrapping round to the oldest, and files
// its key in gone.
func (c *Cache) evict() {
	el := c.hand
	if el == nil {
		el = c.order.Back()
	}
	for el.Value.(*cacheEntry).visited {
		el.Value.(*cacheEntry).visited = false
		if el = el.Prev(); el == nil {
			el = c.order.Back()
		}
	}
	c.hand = el.Prev()
	c.order.Remove(el)
	key := el.Value.(*cacheEntry).key
	delete(c.items, key)

	if c.ring == nil {
		c.ring = make([]uint64, c.max)
	}
	if old := c.ring[c.next]; c.gone[old] == c.next {
		delete(c.gone, old)
	}
	h := maphash.String(c.seed, key)
	c.ring[c.next], c.gone[h] = h, c.next
	c.next = (c.next + 1) % len(c.ring)
}
