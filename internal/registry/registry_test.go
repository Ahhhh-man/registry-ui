package registry_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

// TestHarness shows how to use registrytest: start a seeded registry, then
// talk to it over HTTP.
func TestHarness(t *testing.T) {
	r := registrytest.New(t)
	ctx := t.Context()

	// The catalog comes back one sorted page at a time, linked by a Link header.
	resp, err := http.Get(r.URL + "/v2/_catalog")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("Link") == "" {
		t.Error("first catalog page has no Link header")
	}

	reg, err := name.NewRegistry(r.Host, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	repos, err := remote.Catalog(ctx, reg)
	want := []string{registrytest.RepoHelm, registrytest.RepoEmpty, registrytest.RepoUntagged, registrytest.RepoSingle, registrytest.RepoIndex, registrytest.RepoAllMissing, registrytest.RepoDangling, registrytest.RepoSomeMissing, registrytest.RepoNested, registrytest.RepoPaged}
	if err != nil || !slices.Equal(repos, want) {
		t.Errorf("catalog = %v, %v; want %v", repos, err, want)
	}

	tags, err := remote.List(reg.Repo(registrytest.RepoPaged), remote.WithContext(ctx))
	if err != nil || len(tags) != len(registrytest.PagedTags) {
		t.Errorf("paged tags = %v, %v; want %d tags", tags, err, len(registrytest.PagedTags))
	}

	// Both tags of the alias pair point at the index.
	indexDigest, _ := r.Index.Digest()
	for _, tag := range []string{registrytest.TagVersion, registrytest.TagLatest} {
		d, err := remote.Head(reg.Repo(registrytest.RepoIndex).Tag(tag), remote.WithContext(ctx))
		if err != nil || d.Digest != indexDigest {
			t.Errorf("%s: got %v, %v; want %v", tag, d, err, indexDigest)
		}
	}
}

func TestHarnessBasicAuth(t *testing.T) {
	r := registrytest.NewWithBasicAuth(t, "alice", "secret")
	reg, _ := name.NewRegistry(r.Host, name.Insecure)
	repo := reg.Repo(registrytest.RepoSingle)

	resp, err := http.Get(r.URL + "/v2/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("anonymous: status %d, challenge %q; want 401 with a challenge", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	auth := remote.WithAuth(&authn.Basic{Username: "alice", Password: "secret"})
	if _, err := remote.List(repo, auth, remote.WithContext(t.Context())); err != nil {
		t.Errorf("with credentials: %v", err)
	}
}
