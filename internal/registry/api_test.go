package registry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

func newAPI(t *testing.T, url string) *registry.API {
	t.Helper()
	a, err := registry.NewAPI(url, false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAPIBasicAuth(t *testing.T) {
	r := registrytest.NewWithBasicAuth(t, "alice", "secret")
	a := newAPI(t, r.URL)

	for _, ctx := range []context.Context{t.Context(), registry.WithCredentials(t.Context(), "alice", "wrong")} {
		_, err := a.Catalog(ctx)
		var ue *registry.UnauthorizedError
		if !errors.As(err, &ue) || !ue.Challenged || !errors.Is(err, registry.ErrUnauthorized) {
			t.Errorf("got %v, want a challenged UnauthorizedError", err)
		}
	}

	ctx := registry.WithCredentials(t.Context(), "alice", "secret")
	if _, err := a.Catalog(ctx); err != nil {
		t.Errorf("catalog with credentials: %v", err)
	}
	m, err := a.Resolve(ctx, registrytest.RepoIndex, registrytest.TagLatest)
	if err != nil || len(m.Platforms) != 3 {
		t.Fatalf("resolve with credentials: %+v, %v", m, err)
	}
	if _, err := a.Config(ctx, registrytest.RepoIndex, m.Platforms[0].Config); err != nil {
		t.Errorf("config with credentials: %v", err)
	}
}

// Challenged is set only by a challenge to an anonymous request.
func TestAPIChallenged(t *testing.T) {
	a := newAPI(t, registrytest.NewWithBasicAuth(t, "alice", "secret").URL)
	_, _ = a.Catalog(registry.WithCredentials(t.Context(), "alice", "wrong"))
	if a.Challenged() {
		t.Error("challenged after a signed-in request")
	}
	_, _ = a.Tags(t.Context(), registrytest.RepoIndex)
	if !a.Challenged() {
		t.Error("not challenged after an anonymous request")
	}

	open := newAPI(t, registrytest.New(t).URL)
	if _, err := open.Catalog(t.Context()); err != nil || open.Challenged() {
		t.Errorf("registry without auth: challenged %t, err %v", open.Challenged(), err)
	}
}

func TestAPICheck(t *testing.T) {
	a := newAPI(t, registrytest.NewWithBasicAuth(t, "alice", "secret").URL)
	if err := a.Check(registry.WithCredentials(t.Context(), "alice", "secret")); err != nil {
		t.Errorf("good credentials: %v", err)
	}
	for _, ctx := range []context.Context{t.Context(), registry.WithCredentials(t.Context(), "alice", "wrong")} {
		if err := a.Check(ctx); !errors.Is(err, registry.ErrUnauthorized) {
			t.Errorf("got %v, want ErrUnauthorized", err)
		}
	}
	if err := newAPI(t, registrytest.New(t).URL).Check(t.Context()); err != nil {
		t.Errorf("registry without auth: %v", err)
	}
}

// TestAPICheckToken covers the token-server flow: the registry wants a
// bearer token, which a token server hands out for good basic credentials.
func TestAPICheckToken(t *testing.T) {
	// The token server shares the registry's host: go-containerregistry
	// refuses a realm on another private address.
	var reg *httptest.Server
	reg = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if u, p, _ := r.BasicAuth(); u != "alice" || p != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"t0k3n"}`))
		case r.Header.Get("Authorization") != "Bearer t0k3n":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+reg.URL+`/token",service="test"`)
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(reg.Close)

	a := newAPI(t, reg.URL)
	if err := a.Check(registry.WithCredentials(t.Context(), "alice", "secret")); err != nil {
		t.Errorf("good credentials: %v", err)
	}
	if err := a.Check(registry.WithCredentials(t.Context(), "alice", "wrong")); !errors.Is(err, registry.ErrUnauthorized) {
		t.Errorf("bad credentials: got %v, want ErrUnauthorized", err)
	}
}

// front is a proxy in front of a registry that counts the requests for each
// path and can start refusing them all.
type front struct {
	URL    string
	refuse atomic.Bool // answer 401 with a Basic challenge
	mu     sync.Mutex
	paths  map[string]int
}

func newFront(t *testing.T, registryURL string) *front {
	t.Helper()
	up, err := url.Parse(registryURL)
	if err != nil {
		t.Fatal(err)
	}
	f := &front{paths: map[string]int{}}
	proxy := httputil.NewSingleHostReverseProxy(up)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		f.mu.Lock()
		f.paths[req.URL.Path]++
		f.mu.Unlock()
		if f.refuse.Load() {
			w.Header().Set("WWW-Authenticate", `Basic realm="front"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// pings is how often the registry has been asked how to sign in: GET /v2/.
func (f *front) pings() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paths["/v2/"]
}

// A tag list, a resolve and a config for one repository ask the registry how
// to sign in once between them, not once each.
func TestAPIAsksOncePerRepository(t *testing.T) {
	for name, tc := range map[string]struct {
		reg *registrytest.Registry
		ctx context.Context
	}{
		"anonymous":  {registrytest.New(t), t.Context()},
		"basic auth": {registrytest.NewWithBasicAuth(t, "alice", "secret"), registry.WithCredentials(t.Context(), "alice", "secret")},
	} {
		f := newFront(t, tc.reg.URL)
		a := newAPI(t, f.URL)
		if _, err := a.Tags(tc.ctx, registrytest.RepoIndex); err != nil {
			t.Fatalf("%s: tags: %v", name, err)
		}
		m, err := a.Resolve(tc.ctx, registrytest.RepoIndex, registrytest.TagLatest)
		if err != nil {
			t.Fatalf("%s: resolve: %v", name, err)
		}
		if _, err := a.Config(tc.ctx, registrytest.RepoIndex, m.Platforms[0].Config); err != nil {
			t.Fatalf("%s: config: %v", name, err)
		}
		if n := f.pings(); n != 1 {
			t.Errorf("%s: GET /v2/ sent %d times, want 1", name, n)
		}
	}
}

// What the registry is remembered to have told one user is never used for
// another user or for an anonymous request.
func TestAPIRemembersPerIdentity(t *testing.T) {
	f := newFront(t, registrytest.NewWithBasicAuth(t, "alice", "secret").URL)
	a := newAPI(t, f.URL)
	tags := func(ctx context.Context) error {
		_, err := a.Tags(ctx, registrytest.RepoIndex)
		return err
	}
	alice := registry.WithCredentials(t.Context(), "alice", "secret")
	if err := tags(alice); err != nil {
		t.Fatalf("alice: %v", err)
	}
	// The registry lets only alice in, so a request answered with what she
	// signed in for would get the tags.
	for name, ctx := range map[string]context.Context{
		"anonymous":             t.Context(),
		"bob":                   registry.WithCredentials(t.Context(), "bob", "secret"),
		"alice, wrong password": registry.WithCredentials(t.Context(), "alice", "wrong"),
	} {
		if err := tags(ctx); !errors.Is(err, registry.ErrUnauthorized) {
			t.Errorf("%s after alice: got %v, want ErrUnauthorized", name, err)
		}
	}
	// The others didn't take hers away either.
	before := f.pings()
	if err := tags(alice); err != nil || f.pings() != before {
		t.Errorf("alice again: %v, with %d more GET /v2/", err, f.pings()-before)
	}
}

// A registry that starts answering 401 isn't hidden by what was remembered
// from before: the 401 comes back, and the next call asks the registry how
// to sign in again, as every call used to.
func TestAPIRefusedAfterReuse(t *testing.T) {
	f := newFront(t, registrytest.New(t).URL)
	a := newAPI(t, f.URL)
	if _, err := a.Tags(t.Context(), registrytest.RepoIndex); err != nil {
		t.Fatal(err)
	}

	f.refuse.Store(true)
	_, err := a.Resolve(t.Context(), registrytest.RepoIndex, registrytest.TagLatest)
	if ue := (*registry.UnauthorizedError)(nil); !errors.As(err, &ue) || !ue.Challenged {
		t.Errorf("refused: got %v, want a challenged UnauthorizedError", err)
	}
	if !a.Challenged() {
		t.Error("not challenged after an anonymous request was")
	}

	f.refuse.Store(false)
	if _, err := a.Resolve(t.Context(), registrytest.RepoIndex, registrytest.TagLatest); err != nil {
		t.Errorf("let in again: %v", err)
	}
	if n := f.pings(); n != 2 {
		t.Errorf("GET /v2/ sent %d times, want 2: once at first and once after the 401", n)
	}
}

// With a token server, the token is kept between calls, a new one is
// fetched when the registry stops taking it, and a user the token server
// turns away gets the 401.
func TestAPITokenReuse(t *testing.T) {
	// tokens counts the tokens handed out. The registry takes only the one
	// for the current epoch, so moving the epoch on expires what's held.
	var tokens, epoch atomic.Int32
	var revoked atomic.Bool
	var reg *httptest.Server
	reg = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if u, p, _ := r.BasicAuth(); u != "alice" || p != "secret" || revoked.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			tokens.Add(1)
			_, _ = fmt.Fprintf(w, `{"token":"t%d"}`, epoch.Load())
		case r.Header.Get("Authorization") != fmt.Sprintf("Bearer t%d", epoch.Load()):
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+reg.URL+`/token",service="test"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path != "/v2/":
			_, _ = w.Write([]byte(`{"tags":["1.0.0"]}`))
		}
	}))
	t.Cleanup(reg.Close)

	a := newAPI(t, reg.URL)
	ctx := registry.WithCredentials(t.Context(), "alice", "secret")
	for _, step := range []struct {
		name   string
		expire bool
		want   int32 // tokens handed out by the end of the step
	}{
		{"first call", false, 1},
		{"second call", false, 1},
		{"token expired", true, 2},
	} {
		if step.expire {
			epoch.Add(1)
		}
		if tags, err := a.Tags(ctx, "app"); err != nil || len(tags) != 1 || tokens.Load() != step.want {
			t.Errorf("%s: tags %v, %v, %d tokens fetched; want %d", step.name, tags, err, tokens.Load(), step.want)
		}
	}

	revoked.Store(true)
	epoch.Add(1)
	if _, err := a.Tags(ctx, "app"); !errors.Is(err, registry.ErrUnauthorized) {
		t.Errorf("token server turns alice away: got %v, want ErrUnauthorized", err)
	}
}

func TestAPIErrors(t *testing.T) {
	status := func(code int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	for _, tc := range []struct {
		name, url string
		want      error
	}{
		{"403", status(http.StatusForbidden), registry.ErrForbidden},
		{"500", status(http.StatusInternalServerError), registry.ErrUnavailable},
		{"401 without a challenge", status(http.StatusUnauthorized), registry.ErrUnauthorized},
		{"connection refused", closed.URL, registry.ErrUnavailable},
	} {
		_, err := newAPI(t, tc.url).Catalog(t.Context())
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
		if ue := (*registry.UnauthorizedError)(nil); errors.As(err, &ue) && ue.Challenged {
			t.Errorf("%s: reported a challenge the registry never sent", tc.name)
		}
	}
}

func TestNewAPI(t *testing.T) {
	for _, u := range []string{"", "registry:5000", "ftp://registry", "http://", "http://registry/sub/path"} {
		if _, err := registry.NewAPI(u, false); err == nil {
			t.Errorf("NewAPI(%q) should fail", u)
		}
	}
	for _, u := range []string{"http://registry:5000", "https://registry.example.com/"} {
		if _, err := registry.NewAPI(u, true); err != nil {
			t.Errorf("NewAPI(%q): %v", u, err)
		}
	}
}

// Only a 404 marks a platform missing. Any other failure to fetch one fails
// the whole lookup, rather than passing the platform off as missing.
func TestAPIPlatformErrors(t *testing.T) {
	r := registrytest.New(t)
	up, err := url.Parse(r.URL)
	if err != nil {
		t.Fatal(err)
	}
	arm64 := digest(t, r.Platforms[1])
	for _, code := range []int{http.StatusInternalServerError, http.StatusUnauthorized} {
		proxy := httputil.NewSingleHostReverseProxy(up)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/v2/"+registrytest.RepoIndex+"/manifests/"+arm64 {
				w.WriteHeader(code)
				return
			}
			proxy.ServeHTTP(w, req)
		}))
		t.Cleanup(srv.Close)
		if m, err := newAPI(t, srv.URL).Resolve(t.Context(), registrytest.RepoIndex, registrytest.TagVersion); err == nil || errors.Is(err, registry.ErrNotFound) {
			t.Errorf("%d on a platform: got %+v, %v; want an error other than ErrNotFound", code, m, err)
		}
	}
}
