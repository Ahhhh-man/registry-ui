// Package registrytest runs an in-memory registry seeded with known content,
// for tests that talk to a registry over HTTP. Only test code may import it.
//
// The registry is go-containerregistry's pkg/registry. It answers
// /v2/_catalog, but unsorted and in one page, and its tag lists honour n and
// last without sending a Link header. A shim in front of both pages them the
// way Distribution does: sorted, at most PageSize names per page, with a Link
// header to the next page, so clients have to follow pagination.
//
// NewStorage writes the same content to disk in Distribution's filesystem
// layout instead, for tests of storage mode.
package registrytest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Repositories and tags in the seeded registry.
const (
	// RepoIndex holds Index under TagVersion and its alias TagLatest, plus
	// Signature under SignatureTag.
	RepoIndex  = "library/nginx"
	TagVersion = "1.0.0"
	TagLatest  = "latest"

	// RepoSingle holds Single under TagSingle.
	RepoSingle = "library/busybox"
	TagSingle  = "1.36"

	// RepoHelm holds the Helm chart Helm under TagHelm.
	RepoHelm = "charts/podinfo"
	TagHelm  = "6.7.1"

	// RepoNested is a nested repository holding Single under TagNested.
	RepoNested = "team/web/frontend"
	TagNested  = "main"

	// RepoPaged holds Single under each of PagedTags.
	RepoPaged = "test/paged"

	// RepoSomeMissing holds Index under TagVersion, without its linux/arm64
	// manifest, like a mirror that only fetched the platforms asked for.
	RepoSomeMissing = "missing/some"
	// RepoAllMissing holds Index under TagVersion without any of the
	// manifests it lists.
	RepoAllMissing = "missing/all"

	// PageSize is the most names one catalog or tag-list page returns.
	PageSize = 3

	// MediaHelmConfig is the config media type of a Helm chart.
	MediaHelmConfig types.MediaType = "application/vnd.cncf.helm.config.v1+json"
)

// PagedTags are the tags in RepoPaged: enough for several pages.
var PagedTags = []string{"v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8", "v9", "v10"}

// Registry is a running, seeded registry. The image fields are what was
// pushed, so tests can derive expected digests, sizes and configs from them.
type Registry struct {
	// URL is the base URL, e.g. "http://127.0.0.1:41234".
	URL string
	// Host is the host:port part of URL, as used in image references.
	Host string
	// StoragePath is the storage root NewStorage wrote, the folder a
	// registry has at /var/lib/registry. Empty for a served registry.
	StoragePath string

	// Index is a multi-arch OCI index. Its children are Platforms, in order,
	// and then Attestation.
	Index v1.ImageIndex
	// Platforms are Index's linux/amd64, linux/arm64 and linux/arm/v7 images.
	// Their configs carry the description and source labels.
	Platforms []v1.Image
	// Attestation is Index's buildx-style attestation manifest, listed as
	// unknown/unknown.
	Attestation v1.Image
	// Single is a single-platform linux/amd64 image without labels.
	Single v1.Image
	// Signature is a cosign-style signature of Index.
	Signature v1.Image
	// SignatureTag is "sha256-<hex of Index's digest>.sig".
	SignatureTag string
	// Helm is a Helm chart with config media type MediaHelmConfig.
	Helm v1.Image
}

// Labels and created times in the seeded image configs.
var (
	PlatformsCreated = time.Date(2024, 11, 20, 10, 30, 0, 0, time.UTC)
	SingleCreated    = time.Date(2023, 5, 1, 8, 0, 0, 0, time.UTC)
	PlatformsLabels  = map[string]string{
		"org.opencontainers.image.description": "A seeded multi-arch test image.",
		"org.opencontainers.image.source":      "https://example.com/nginx",
	}
)

// New starts a seeded registry that allows anonymous access. It shuts down
// when the test ends.
func New(t testing.TB) *Registry { return start(t, "", "") }

// NewWithBasicAuth is New, but every request must carry the given basic-auth
// credentials. Others get a 401 with a Basic WWW-Authenticate challenge.
func NewWithBasicAuth(t testing.TB, user, pass string) *Registry { return start(t, user, pass) }

func start(t testing.TB, user, pass string) *Registry {
	t.Helper()

	h := paginate(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	if user != "" {
		h = requireBasicAuth(h, user, pass)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	r := &Registry{URL: srv.URL, Host: strings.TrimPrefix(srv.URL, "http://")}
	r.build(t)

	opts := []remote.Option{remote.WithContext(t.Context())}
	if user != "" {
		opts = append(opts, remote.WithAuth(&authn.Basic{Username: user, Password: pass}))
	}
	for _, c := range r.content() {
		ref, err := name.NewTag(r.Host+"/"+c.repo+":"+c.tag, name.Insecure)
		if err == nil {
			switch obj := c.obj.(type) {
			case v1.ImageIndex:
				err = remote.WriteIndex(ref, obj, opts...)
			case v1.Image:
				err = remote.Write(ref, obj, opts...)
			}
		}
		// The registry won't take an index without its manifests, so they
		// go in with it and are deleted after, like garbage collection does.
		for _, h := range c.missing {
			if err == nil {
				err = remote.Delete(ref.Digest(h.String()), opts...)
			}
		}
		if err != nil {
			t.Fatalf("registrytest: push %s:%s: %v", c.repo, c.tag, err)
		}
	}
	return r
}

// NewStorage writes the same seeded content as New into a temporary folder,
// laid out the way Distribution's filesystem storage driver lays it out, and
// returns it with StoragePath set. Nothing is served, so URL and Host are
// empty.
func NewStorage(t testing.TB) *Registry {
	t.Helper()
	r := &Registry{StoragePath: t.TempDir()}
	r.build(t)
	v2 := filepath.Join(r.StoragePath, "docker", "registry", "v2")
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// blob stores data once and links it into repo under dir.
	blob := func(repo, dir string, data []byte) v1.Hash {
		t.Helper()
		sum := sha256.Sum256(data)
		h := v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])}
		write(filepath.Join(v2, "blobs", "sha256", h.Hex[:2], h.Hex, "data"), string(data))
		write(filepath.Join(v2, "repositories", repo, dir, "sha256", h.Hex, "link"), h.String())
		return h
	}
	image := func(repo string, img v1.Image) v1.Hash {
		t.Helper()
		blob(repo, "_layers", must(img.RawConfigFile()))
		for _, l := range must(img.Layers()) {
			rc := must(l.Compressed())
			blob(repo, "_layers", must(io.ReadAll(rc)))
			_ = rc.Close()
		}
		return blob(repo, "_manifests/revisions", must(img.RawManifest()))
	}
	for _, c := range r.content() {
		var h v1.Hash
		switch obj := c.obj.(type) {
		case v1.ImageIndex:
			for _, d := range must(obj.IndexManifest()).Manifests {
				if !slices.Contains(c.missing, d.Digest) {
					image(c.repo, must(obj.Image(d.Digest)))
				}
			}
			h = blob(c.repo, "_manifests/revisions", must(obj.RawManifest()))
		case v1.Image:
			h = image(c.repo, obj)
		}
		tag := filepath.Join(v2, "repositories", c.repo, "_manifests", "tags", c.tag)
		write(filepath.Join(tag, "current", "link"), h.String())
		write(filepath.Join(tag, "index", "sha256", h.Hex, "link"), h.String())
	}
	return r
}

// SetTagTime sets when repo:tag last changed in a NewStorage registry, the
// modification time of the tag's link, as a push or a pull-through would.
func (r *Registry) SetTagTime(t testing.TB, repo, tag string, when time.Time) {
	t.Helper()
	link := filepath.Join(r.StoragePath, "docker", "registry", "v2", "repositories", repo, "_manifests", "tags", tag, "current", "link")
	if err := os.Chtimes(link, when, when); err != nil {
		t.Fatal(err)
	}
}

// content lists every tag in the seeded registry, in push order.
func (r *Registry) content() []tagged {
	c := []tagged{
		{repo: RepoIndex, tag: TagVersion, obj: r.Index},
		{repo: RepoIndex, tag: TagLatest, obj: r.Index},
		{repo: RepoIndex, tag: r.SignatureTag, obj: r.Signature},
		{repo: RepoSingle, tag: TagSingle, obj: r.Single},
		{repo: RepoHelm, tag: TagHelm, obj: r.Helm},
		{repo: RepoNested, tag: TagNested, obj: r.Single},
	}
	for _, tag := range PagedTags {
		c = append(c, tagged{repo: RepoPaged, tag: tag, obj: r.Single})
	}
	children := must(r.Index.IndexManifest()).Manifests
	c = append(c,
		tagged{repo: RepoSomeMissing, tag: TagVersion, obj: r.Index, missing: []v1.Hash{children[1].Digest}},
		tagged{repo: RepoAllMissing, tag: TagVersion, obj: r.Index, missing: []v1.Hash{children[0].Digest, children[1].Digest, children[2].Digest, children[3].Digest}},
	)
	return c
}

type tagged struct {
	repo, tag string
	obj       any // v1.Image or v1.ImageIndex
	// missing are manifests of an index left out of the registry.
	missing []v1.Hash
}

// must panics on an error building fixtures from in-memory images, which
// can only be a bug in this package.
func must[T any](v T, err error) T {
	if err != nil {
		panic("registrytest: build fixtures: " + err.Error())
	}
	return v
}

// build makes the seed content. It is deterministic, so digests are the same
// on every run.
func (r *Registry) build(t testing.TB) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("registrytest: build fixtures: %v", err)
		}
	}

	// image is an OCI image with one layer of the given content and type.
	image := func(layer string, layerType types.MediaType, cfg v1.ConfigFile) v1.Image {
		img, err := mutate.Append(empty.Image, mutate.Addendum{Layer: static.NewLayer([]byte(layer), layerType)})
		must(err)
		img = mutate.ConfigMediaType(mutate.MediaType(img, types.OCIManifestSchema1), types.OCIConfigJSON)
		img, err = mutate.ConfigFile(img, &cfg)
		must(err)
		return img
	}
	digest := func(img v1.Image) v1.Hash {
		d, err := img.Digest()
		must(err)
		return d
	}

	idx := mutate.IndexMediaType(empty.Index, types.OCIImageIndex)
	for _, p := range []v1.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64"}, {OS: "linux", Architecture: "arm", Variant: "v7"}} {
		img := image("nginx for "+p.String(), types.OCILayer, v1.ConfigFile{
			OS: p.OS, Architecture: p.Architecture, Variant: p.Variant,
			Created: v1.Time{Time: PlatformsCreated},
			Config:  v1.Config{Labels: maps.Clone(PlatformsLabels)},
		})
		r.Platforms = append(r.Platforms, img)
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &p}})
	}
	r.Attestation = image(`{"_type":"https://in-toto.io/Statement/v0.1"}`, "application/vnd.in-toto+json", v1.ConfigFile{})
	r.Index = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: r.Attestation, Descriptor: v1.Descriptor{
		Platform: &v1.Platform{OS: "unknown", Architecture: "unknown"},
		Annotations: map[string]string{
			"vnd.docker.reference.type":   "attestation-manifest",
			"vnd.docker.reference.digest": digest(r.Platforms[0]).String(),
		},
	}})
	indexDigest, err := r.Index.Digest()
	must(err)

	r.Single = image("busybox", types.OCILayer, v1.ConfigFile{
		OS: "linux", Architecture: "amd64",
		Created: v1.Time{Time: SingleCreated},
	})

	r.SignatureTag = "sha256-" + indexDigest.Hex + ".sig"
	r.Signature = image(
		fmt.Sprintf(`{"critical":{"image":{"docker-manifest-digest":%q},"type":"cosign container image signature"}}`, indexDigest),
		"application/vnd.dev.cosign.simplesigning.v1+json", v1.ConfigFile{})

	// The config blob is an image config rather than Chart.yaml as JSON;
	// only the media type matters here.
	r.Helm = mutate.ConfigMediaType(
		image("podinfo-6.7.1.tgz", "application/vnd.cncf.helm.chart.content.v1.tar+gzip", v1.ConfigFile{}),
		MediaHelmConfig)
}

// paginate pages catalog and tag-list responses the way Distribution does.
func paginate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		catalog := req.URL.Path == "/v2/_catalog"
		if req.Method != http.MethodGet || !catalog && !strings.HasSuffix(req.URL.Path, "/tags/list") {
			next.ServeHTTP(w, req)
			return
		}

		// Ask for the whole list, then cut the page from it.
		all := req.Clone(req.Context())
		all.URL.RawQuery = ""
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, all)

		var body struct {
			Name         string   `json:"name"`
			Repositories []string `json:"repositories"`
			Tags         []string `json:"tags"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			maps.Copy(w.Header(), rec.Header())
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		names := body.Tags
		if catalog {
			names = body.Repositories
		}
		names = slices.Sorted(slices.Values(names))

		q := req.URL.Query()
		if last := q.Get("last"); last != "" {
			names = names[sort.Search(len(names), func(i int) bool { return names[i] > last }):]
		}
		n := PageSize
		if v, err := strconv.Atoi(q.Get("n")); err == nil && v > 0 && v < n {
			n = v
		}
		if len(names) > n {
			names = names[:n]
			link := url.URL{Path: req.URL.Path, RawQuery: url.Values{"n": {strconv.Itoa(n)}, "last": {names[n-1]}}.Encode()}
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, link.String()))
		}

		var out any = map[string]any{"name": body.Name, "tags": names}
		if catalog {
			out = map[string]any{"repositories": names}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// requireBasicAuth answers 401 with a Basic challenge unless the request
// carries the given credentials.
func requireBasicAuth(next http.Handler, user, pass string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if u, p, ok := req.BasicAuth(); ok && u == user && p == pass {
			next.ServeHTTP(w, req)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="registrytest"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`)
	})
}
