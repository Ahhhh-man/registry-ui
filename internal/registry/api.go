package registry

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const (
	// requestTimeout bounds each upstream request, body included.
	requestTimeout = 10 * time.Second
	// maxConfigSize caps how much of a config blob is read.
	maxConfigSize = 4 << 20

	mediaHelmConfig = "application/vnd.cncf.helm.config.v1+json"
)

// Reference grammar from the OCI distribution spec, checked before a name
// reaches a URL. Only sha256 digests are accepted.
var (
	repoName  = regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*$`)
	tagName   = regexp.MustCompile(`^\w[\w.-]{0,127}$`)
	digestRef = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// sigTag matches cosign's signature, attestation and SBOM tags.
var sigTag = regexp.MustCompile(`^sha256-[0-9a-f]{64}\.(sig|att|sbom)$`)

// signatureMediaPrefixes mark the config or layers of signatures and
// attestations.
var signatureMediaPrefixes = []string{
	"application/vnd.dev.cosign.",
	"application/vnd.dev.sigstore.",
	"application/vnd.dsse.envelope.",
	"application/vnd.in-toto+json",
}

// API is a Source that reads a registry through its HTTP API. Credentials
// come from the context (see WithCredentials).
type API struct {
	reg       name.Registry
	transport http.RoundTripper
}

var _ Source = (*API)(nil)

// NewAPI returns a Source for the registry at registryURL, e.g.
// "http://registry:5000". insecure skips TLS certificate verification.
func NewAPI(registryURL string, insecure bool) (*API, error) {
	u, err := url.Parse(registryURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Trim(u.Path, "/") != "" {
		return nil, fmt.Errorf("registry URL %q: want http(s)://host[:port]", registryURL)
	}
	var opts []name.Option
	if u.Scheme == "http" {
		opts = append(opts, name.Insecure)
	}
	a := &API{}
	if a.reg, err = name.NewRegistry(u.Host, opts...); err != nil {
		return nil, fmt.Errorf("registry URL %q: %w", registryURL, err)
	}
	t := remote.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	a.transport = t
	return a, nil
}

// Catalog implements Source.
func (a *API) Catalog(ctx context.Context) ([]string, error) {
	c := a.call(ctx)
	repos, err := remote.Catalog(ctx, a.reg, c.opts...)
	if err != nil {
		return nil, c.fail("catalog", err)
	}
	slices.Sort(repos)
	return repos, nil
}

// Tags implements Source.
func (a *API) Tags(ctx context.Context, repo string) ([]string, error) {
	r, err := a.repo(repo)
	if err != nil {
		return nil, err
	}
	c := a.call(ctx)
	tags, err := remote.List(r, c.opts...)
	if err != nil {
		return nil, c.fail("tags of "+repo, err)
	}
	return tags, nil
}

// Resolve implements Source.
func (a *API) Resolve(ctx context.Context, repo, ref string) (Manifest, error) {
	r, err := a.repo(repo)
	if err != nil {
		return Manifest{}, err
	}
	var target name.Reference
	switch {
	case digestRef.MatchString(ref):
		target = r.Digest(ref)
	case tagName.MatchString(ref):
		target = r.Tag(ref)
	default:
		return Manifest{}, fmt.Errorf("manifest %s:%s: %w", repo, ref, ErrNotFound)
	}

	c := a.call(ctx)
	// One puller per call reuses the ping and token across its requests.
	p, err := remote.NewPuller(c.opts...)
	if err != nil {
		return Manifest{}, err
	}
	m, err := resolve(ctx, p, r, target)
	if err != nil {
		return Manifest{}, c.fail("manifest "+repo+":"+ref, err)
	}
	return m, nil
}

func resolve(ctx context.Context, p *remote.Puller, repo name.Repository, ref name.Reference) (Manifest, error) {
	desc, err := p.Get(ctx, ref)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Digest: desc.Digest.String(), MediaType: string(desc.MediaType)}
	tag := ""
	if t, ok := ref.(name.Tag); ok {
		tag = t.TagStr()
	}

	if desc.MediaType.IsIndex() {
		idx, err := v1.ParseIndexManifest(bytes.NewReader(desc.Manifest))
		if err != nil {
			return Manifest{}, err
		}
		m.Kind = KindIndex
		if sigTag.MatchString(tag) {
			m.Kind = KindSignature
			return m, nil
		}
		var children []v1.Descriptor
		for _, d := range idx.Manifests {
			if d.MediaType.IsImage() && !isAttestation(d) {
				children = append(children, d)
			}
		}
		m.Platforms = make([]Platform, len(children))
		errs := make([]error, len(children))
		var wg sync.WaitGroup
		for i, d := range children {
			wg.Go(func() { m.Platforms[i], errs[i] = platform(ctx, p, repo, d) })
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return Manifest{}, err
		}
		return m, nil
	}

	mf, err := v1.ParseManifest(bytes.NewReader(desc.Manifest))
	if err != nil {
		return Manifest{}, err
	}
	m.Size = size(mf)
	m.Kind = kind(tag, mf)
	if m.Kind != KindImage {
		return m, nil
	}
	cfg, err := config(ctx, p, repo, mf.Config.Digest.String())
	if err != nil {
		return Manifest{}, err
	}
	m.Platforms = []Platform{{
		OS: cfg.OS, Arch: cfg.Architecture, Variant: cfg.Variant,
		Digest: m.Digest, Config: mf.Config.Digest.String(), Size: m.Size,
	}}
	return m, nil
}

// platform summarises one image listed in an index.
func platform(ctx context.Context, p *remote.Puller, repo name.Repository, d v1.Descriptor) (Platform, error) {
	desc, err := p.Get(ctx, repo.Digest(d.Digest.String()))
	if err != nil {
		return Platform{}, err
	}
	mf, err := v1.ParseManifest(bytes.NewReader(desc.Manifest))
	if err != nil {
		return Platform{}, err
	}
	pl := Platform{Digest: d.Digest.String(), Config: mf.Config.Digest.String(), Size: size(mf)}
	if d.Platform != nil {
		pl.OS, pl.Arch, pl.Variant = d.Platform.OS, d.Platform.Architecture, d.Platform.Variant
	}
	return pl, nil
}

// Config implements Source.
func (a *API) Config(ctx context.Context, repo, digest string) (ImageConfig, error) {
	r, err := a.repo(repo)
	if err != nil {
		return ImageConfig{}, err
	}
	c := a.call(ctx)
	p, err := remote.NewPuller(c.opts...)
	if err != nil {
		return ImageConfig{}, err
	}
	cfg, err := config(ctx, p, r, digest)
	if err != nil {
		return ImageConfig{}, c.fail("config "+repo+"@"+digest, err)
	}
	return ImageConfig{Created: cfg.Created.Time, Labels: cfg.Config.Labels}, nil
}

func config(ctx context.Context, p *remote.Puller, repo name.Repository, digest string) (*v1.ConfigFile, error) {
	if !digestRef.MatchString(digest) {
		return nil, ErrNotFound
	}
	layer, err := p.Layer(ctx, repo.Digest(digest))
	if err != nil {
		return nil, err
	}
	rc, err := layer.Compressed()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return v1.ParseConfigFile(io.LimitReader(rc, maxConfigSize))
}

// repo checks a repository name. A name that can't be valid can't exist, so
// it is reported as not found.
func (a *API) repo(repo string) (name.Repository, error) {
	if !repoName.MatchString(repo) {
		return name.Repository{}, fmt.Errorf("repository %s: %w", repo, ErrNotFound)
	}
	return a.reg.Repo(repo), nil
}

// kind classifies a non-index manifest.
func kind(tag string, m *v1.Manifest) Kind {
	if sigTag.MatchString(tag) || isSignatureMedia(m.Config.MediaType) || isSignatureMedia(types.MediaType(m.ArtifactType)) {
		return KindSignature
	}
	for _, l := range m.Layers {
		if isSignatureMedia(l.MediaType) {
			return KindSignature
		}
	}
	switch {
	case m.Config.MediaType == mediaHelmConfig:
		return KindHelm
	case m.ArtifactType != "" || !m.Config.MediaType.IsConfig():
		return KindArtifact
	}
	return KindImage
}

func isSignatureMedia(mt types.MediaType) bool {
	return slices.ContainsFunc(signatureMediaPrefixes, func(p string) bool { return strings.HasPrefix(string(mt), p) })
}

// isAttestation reports whether an index entry is a buildx attestation
// manifest rather than a runnable image.
func isAttestation(d v1.Descriptor) bool {
	return d.Platform != nil && d.Platform.OS == "unknown" && d.Platform.Architecture == "unknown" &&
		d.Annotations["vnd.docker.reference.type"] == "attestation-manifest"
}

// size is a manifest's compressed size: its config plus its layers.
func size(m *v1.Manifest) int64 {
	n := m.Config.Size
	for _, l := range m.Layers {
		n += l.Size
	}
	return n
}

// call is one Source call's view of the registry: its remote options, and
// whether the registry has sent it an auth challenge.
type call struct {
	opts       []remote.Option
	inner      http.RoundTripper
	challenged atomic.Bool
}

func (a *API) call(ctx context.Context) *call {
	c := &call{inner: a.transport}
	auth := authn.Anonymous
	if cr, ok := ctx.Value(credentialsKey{}).(credentials); ok {
		auth = &authn.Basic{Username: cr.user, Password: cr.pass}
	}
	c.opts = []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuth(auth),
		remote.WithTransport(c),
		// Fail fast: a page waiting on retries is worse than an error page.
		remote.WithRetryStatusCodes(),
		remote.WithRetryPredicate(func(error) bool { return false }),
	}
	return c
}

// RoundTrip bounds each request to requestTimeout and notes auth challenges.
func (c *call) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), requestTimeout)
	resp, err := c.inner.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") != "" {
		c.challenged.Store(true)
	}
	resp.Body = cancelOnClose{resp.Body, cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnClose) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

// fail maps a go-containerregistry error onto this package's errors.
func (c *call) fail(what string, err error) error {
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%s: %w", what, err)
	}
	var te *transport.Error
	if errors.As(err, &te) {
		switch te.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%s: %w", what, ErrNotFound)
		case http.StatusUnauthorized:
			return &UnauthorizedError{Challenged: c.challenged.Load()}
		case http.StatusForbidden:
			return fmt.Errorf("%s: %w", what, ErrForbidden)
		}
	}
	return fmt.Errorf("%s: %w: %w", what, ErrUnavailable, err)
}
