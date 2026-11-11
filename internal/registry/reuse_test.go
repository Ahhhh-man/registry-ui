package registry

import (
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

// sent counts the requests of an API that the registry answered, by path.
// The https attempt go-containerregistry makes on an http registry before it
// settles on http isn't one.
type sent struct {
	inner http.RoundTripper
	mu    sync.Mutex
	paths map[string]int
}

// RoundTrip implements http.RoundTripper.
func (s *sent) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := s.inner.RoundTrip(req)
	if err == nil {
		s.mu.Lock()
		s.paths[req.URL.Path]++
		s.mu.Unlock()
	}
	return resp, err
}

// to is how many requests went to paths containing part.
func (s *sent) to(part string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for path, count := range s.paths {
		if strings.Contains(path, part) {
			n += count
		}
	}
	return n
}

// unkept is a transport that closes each connection after use. An idle one
// would hold a goroutine in the synctest bubble and stop its clock.
func unkept() *http.Transport { return &http.Transport{DisableKeepAlives: true} }

// cachedAPI returns a Cache in front of an API that reads r, and the count
// of the requests the API sends.
func cachedAPI(t *testing.T, r *registrytest.Registry) (*Cache, *sent) {
	t.Helper()
	a, err := NewAPI(r.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &sent{inner: roundTripper{unkept()}, paths: map[string]int{}}
	a.transport = s
	return NewCache(a), s
}

// push tags obj, a v1.Image or v1.ImageIndex, in r.
func push(t *testing.T, r *registrytest.Registry, repo, tag string, obj any) {
	t.Helper()
	ref, err := name.NewTag(r.Host+"/"+repo+":"+tag, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	opts := []remote.Option{remote.WithContext(t.Context()), remote.WithTransport(unkept())}
	switch obj := obj.(type) {
	case v1.ImageIndex:
		err = remote.WriteIndex(ref, obj, opts...)
	case v1.Image:
		err = remote.Write(ref, obj, opts...)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func hash(t *testing.T, x interface{ Digest() (v1.Hash, error) }) string {
	t.Helper()
	d, err := x.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

// Once a tag lookup's lifetime has passed, only the tag is asked for again:
// the platform manifests it led to are addressed by digest. A tag that has
// moved is still followed.
func TestCacheReusesPlatforms(t *testing.T) {
	r := registrytest.New(t)
	synctest.Test(t, func(t *testing.T) {
		c, sent := cachedAPI(t, r)
		const repo, tag = "test/four", "latest"
		four := mutate.AppendManifests(r.Index, mutate.IndexAddendum{
			Add: r.Single, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "riscv64"}},
		})
		push(t, r, repo, tag, four)

		first, err := c.Resolve(t.Context(), repo, tag)
		if err != nil || len(first.Platforms) != 4 {
			t.Fatalf("resolve: %+v, %v", first, err)
		}
		if n := sent.to("/manifests/"); n != 5 {
			t.Errorf("first lookup sent %d manifest requests, want 5: the index and its four platforms", n)
		}

		time.Sleep(cacheTTL)
		again, err := c.Resolve(t.Context(), repo, tag)
		if err != nil || !reflect.DeepEqual(again, first) {
			t.Errorf("after the lifetime: %+v, %v; want %+v", again, err, first)
		}
		if n := sent.to("/manifests/") - 5; n != 1 {
			t.Errorf("lookup after the lifetime sent %d manifest requests, want 1", n)
		}

		push(t, r, repo, tag, r.Single)
		time.Sleep(cacheTTL)
		moved, err := c.Resolve(t.Context(), repo, tag)
		if err != nil || moved.Digest != hash(t, r.Single) || moved.Kind != KindImage || len(moved.Platforms) != 1 || moved.Platforms[0].Arch != "amd64" {
			t.Errorf("after the tag moved: %+v, %v; want the single image", moved, err)
		}
	})
}

// Resolve reads a single-platform image's config for its OS and
// architecture. The Config call that follows doesn't fetch it again, and
// nor does the next lookup of the tag.
func TestCacheReusesConfig(t *testing.T) {
	r := registrytest.New(t)
	synctest.Test(t, func(t *testing.T) {
		c, sent := cachedAPI(t, r)
		m, err := c.Resolve(t.Context(), registrytest.RepoSingle, registrytest.TagSingle)
		if err != nil || len(m.Platforms) != 1 {
			t.Fatalf("resolve: %+v, %v", m, err)
		}
		cfg, err := c.Config(t.Context(), registrytest.RepoSingle, m.Platforms[0].Config)
		if err != nil || !cfg.Created.Equal(registrytest.SingleCreated) {
			t.Errorf("config: %+v, %v", cfg, err)
		}
		if n := sent.to("/blobs/"); n != 1 {
			t.Errorf("Resolve then Config fetched the config %d times, want 1", n)
		}

		time.Sleep(cacheTTL)
		again, err := c.Resolve(t.Context(), registrytest.RepoSingle, registrytest.TagSingle)
		if err != nil || !reflect.DeepEqual(again, m) {
			t.Errorf("after the lifetime: %+v, %v; want %+v", again, err, m)
		}
		if blobs, manifests := sent.to("/blobs/"), sent.to("/manifests/"); blobs != 1 || manifests != 2 {
			t.Errorf("by the second lookup the config was fetched %d times and the manifest %d, want 1 and 2", blobs, manifests)
		}

		// What Resolve read for one user isn't served to another.
		alice := WithCredentials(t.Context(), "alice", "secret")
		if _, err := c.Config(alice, registrytest.RepoSingle, m.Platforms[0].Config); err != nil || sent.to("/blobs/") != 2 {
			t.Errorf("config as another user: %v, with the config fetched %d times in all; want 2", err, sent.to("/blobs/"))
		}
	})
}

// A platform the registry didn't have is asked for again on every lookup,
// so it shows up once a mirror has fetched it. The others aren't.
func TestCacheRefetchesMissingPlatform(t *testing.T) {
	r := registrytest.New(t)
	synctest.Test(t, func(t *testing.T) {
		c, sent := cachedAPI(t, r)
		const repo, tag = registrytest.RepoSomeMissing, registrytest.TagVersion
		m, err := c.Resolve(t.Context(), repo, tag)
		if err != nil || len(m.Platforms) != 3 || !m.Platforms[1].Missing {
			t.Fatalf("resolve: %+v, %v; want linux/arm64 missing", m, err)
		}
		before := sent.to("/manifests/")

		push(t, r, repo, "arm64", r.Platforms[1])
		time.Sleep(cacheTTL)
		m, err = c.Resolve(t.Context(), repo, tag)
		if err != nil || len(m.Platforms) != 3 {
			t.Fatalf("resolve again: %+v, %v", m, err)
		}
		if p := m.Platforms[1]; p.Missing || p.Digest != hash(t, r.Platforms[1]) || p.Config == "" || p.Size == 0 {
			t.Errorf("linux/arm64 after the registry got it: %+v", p)
		}
		if n := sent.to("/manifests/") - before; n != 2 {
			t.Errorf("second lookup sent %d manifest requests, want 2: the index and the platform that was missing", n)
		}
	})
}
