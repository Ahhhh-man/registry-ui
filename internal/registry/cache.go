package registry

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/maphash"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sync/singleflight"
)

const (
	// cacheSize is about how many bytes the cache's entries take, as
	// entrySize estimates them, before it evicts one. A catalog sort or
	// type filter needs one entry per repository (its summary, about 600
	// bytes) and a repository's tag sort one per tag (its summary, about
	// 300), plus the page of cards shown, for each signed-in user. Other
	// realistic entries come to 250–1,000 bytes. This fits one user's sort
	// of about 5,000 repositories or tags, and keeps the cache within the
	// 15 MB idle memory budget. An entry bigger than a tenth of this isn't
	// kept, so one huge tag list can't push everything else out.
	cacheSize = 3_500_000
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
	max   int           // bytes
	used  int           // bytes, by entrySize
	order *list.List    // of *cacheEntry, newest first
	hand  *list.Element // where the next eviction starts looking; nil: the oldest
	items map[string]*list.Element
	// gone holds hashes of the last evicted keys, as many as the cache held
	// when it first evicted, each mapped to its slot in ring. A key that
	// comes back while it's here is being read in a cycle longer than the
	// cache holds, such as a catalog sort, so it isn't kept the first time:
	// keeping it would evict what the cycle needs next.
	seed maphash.Seed
	gone map[uint64]int
	ring []uint64
	next int
}

type cacheEntry struct {
	key     string
	val     any
	size    int
	expires time.Time // zero: until evicted
	visited bool
}

var _ Deleter = (*Cache)(nil)

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

// Digest implements Source. A tag's fresh lookup already knows its digest.
func (c *Cache) Digest(ctx context.Context, repo, tag string) (string, error) {
	if m, fresh := c.get(entryKey(ctx, "manifest", repo, tag)); fresh {
		return m.(Manifest).Digest, nil
	}
	return cached(ctx, c, cacheTTL, func(ctx context.Context) (string, error) { return c.src.Digest(ctx, repo, tag) }, "digest", repo, tag)
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

// History implements Source without caching: only the history panel asks,
// once per visit, and an image's history can be much bigger than its
// summary.
func (c *Cache) History(ctx context.Context, repo, digest string) (History, error) {
	return c.src.History(ctx, repo, digest)
}

// CanDelete implements Deleter. Its answer is kept like a tag list's, so
// the list view can ask on every load; a Source that can't delete, such as
// storage, answers ErrDeleteDisabled.
func (c *Cache) CanDelete(ctx context.Context, repo string) error {
	d, ok := c.src.(Deleter)
	if !ok {
		return ErrDeleteDisabled
	}
	v, err := cached(ctx, c, cacheTTL, func(ctx context.Context) (canDelete, error) {
		err := d.CanDelete(ctx, repo)
		if err == nil || errors.Is(err, ErrDeleteDisabled) || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrForbidden) {
			return canDelete{err}, nil // an answer, unlike a registry that failed
		}
		return canDelete{}, err
	}, "can delete", repo)
	if err != nil {
		return err
	}
	return v.err
}

// canDelete is a CanDelete answer, kept as a value since errors aren't.
type canDelete struct{ err error }

// CacheSize implements Sizer.
func (canDelete) CacheSize() int { return 16 }

// Delete implements Deleter, then forgets everything about repo, for
// every identity, so nobody is shown the tags it removed. It forgets even
// when the delete fails, which may have happened anyway.
func (c *Cache) Delete(ctx context.Context, repo, digest string) error {
	d, ok := c.src.(Deleter)
	if !ok {
		return ErrDeleteDisabled
	}
	defer c.forget(repo)
	return d.Delete(ctx, repo, digest)
}

// forget drops every entry about repo, for every identity: its tags, their
// lookups, manifests and times, and the summaries of the repository and
// its tags. A scan, which is fine for something as rare as a delete.
func (c *Cache) forget(repo string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, el := range c.items {
		if about(k) == repo {
			c.remove(el)
		}
	}
}

// about is the repository an entry's key is about, "" for none. A key is
// the identity, then the kind ("summary" and its own kind for a Summary),
// then usually the repository; see entryKey and identity.
func about(key string) string {
	n := 1 // "a"
	if key[0] == 'u' {
		n += sha256.Size
	}
	parts := strings.Split(key[n:], "\x00") // starts with a separator: parts[0] is ""
	if len(parts) > 2 && parts[1] == "summary" {
		parts = parts[1:]
	}
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// Summary returns what summarise makes of src, which is filed under key and
// kept for cacheTTL when src is a Cache. summarise then reads through a
// throwaway cache of its own, so the answers a summary is built from cost
// no entries beside it. Like any entry, a summary is scoped to ctx's
// identity and not kept when summarise fails.
func Summary[T Sizer](ctx context.Context, src Source, summarise func(context.Context, Source) (T, error), key ...string) (T, error) {
	c, ok := src.(*Cache)
	if !ok {
		return summarise(ctx, src)
	}
	return cached(ctx, c, cacheTTL, func(ctx context.Context) (T, error) {
		return summarise(ctx, NewCache(c.src))
	}, append([]string{"summary"}, key...)...)
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
	e := &cacheEntry{key: key, val: val, size: entrySize(key, val)}
	if ttl > 0 {
		e.expires = time.Now().Add(ttl)
	}
	el, ok := c.items[key]
	if ok {
		c.remove(el)
		e.visited = true
	}
	if e.size > cacheSize/10 {
		return
	}
	if !ok && c.used+e.size > c.max {
		h := maphash.String(c.seed, key)
		if _, ok := c.gone[h]; ok {
			delete(c.gone, h)
			return
		}
	}
	for c.used+e.size > c.max {
		c.evict()
	}
	c.used += e.size
	c.items[key] = c.order.PushFront(e)
}

// remove drops el from the cache.
func (c *Cache) remove(el *list.Element) {
	if c.hand == el {
		c.hand = el.Prev()
	}
	e := el.Value.(*cacheEntry)
	c.order.Remove(el)
	delete(c.items, e.key)
	c.used -= e.size
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
	c.remove(el)
	key := el.Value.(*cacheEntry).key

	if c.ring == nil {
		c.ring = make([]uint64, c.order.Len()+1)
	}
	if old := c.ring[c.next]; c.gone[old] == c.next {
		delete(c.gone, old)
	}
	h := maphash.String(c.seed, key)
	c.ring[c.next], c.gone[h] = h, c.next
	c.next = (c.next + 1) % len(c.ring)
}

// entryOverhead estimates what an entry costs besides its key and value:
// its list element, its cacheEntry, its slot in items, and once evicted,
// in gone and ring.
const entryOverhead = 200

// Sizer is a value that roughly estimates the bytes it takes, as entrySize
// does, so the cache can hold it as a Summary.
type Sizer interface{ CacheSize() int }

// entrySize roughly estimates the bytes an entry for key holding val
// takes: fixed-size fields, string contents, slice elements and map
// entries. Every type the cache holds needs a case, and a new string, slice
// or map field needs adding to its type's case. TestEntrySize fails for a
// Source method whose answer has no case.
func entrySize(key string, val any) int {
	n := entryOverhead + len(key)
	switch v := val.(type) {
	case Sizer:
		n += v.CacheSize()
	case []string:
		n += int(unsafe.Sizeof(v))
		for _, s := range v {
			n += int(unsafe.Sizeof(s)) + len(s)
		}
	case string:
		n += int(unsafe.Sizeof(v)) + len(v)
	case Manifest:
		n += int(unsafe.Sizeof(v)) + len(v.Digest) + len(v.MediaType)
		for _, p := range v.Platforms {
			n += int(unsafe.Sizeof(p)) + len(p.OS) + len(p.Arch) + len(p.Variant) + len(p.Digest) + len(p.MediaType) + len(p.Config)
		}
	case ImageConfig:
		n += int(unsafe.Sizeof(v))
		for k, l := range v.Labels {
			n += 48 + len(k) + len(l) // two string headers and the map's own share
		}
	case time.Time:
		n += int(unsafe.Sizeof(v))
	case int64:
		n += int(unsafe.Sizeof(v))
	default:
		panic(fmt.Sprintf("registry: no size estimate for a cached %T", val))
	}
	return n
}
