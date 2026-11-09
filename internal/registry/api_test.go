package registry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
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
