package registry

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"maps"
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
	// maxRemembered is how many repositories the kept pullers remember
	// between them before the least recently used are dropped. A puller
	// keeps about 1 KB for every repository name it is asked about, and
	// visitors pick those names.
	maxRemembered = 10_000

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

// IsSignatureTag reports whether tag names a cosign signature, attestation
// or SBOM: "sha256-<hex>.sig", ".att" or ".sbom".
func IsSignatureTag(tag string) bool { return sigTag.MatchString(tag) }

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
	// challenged is set once the registry sends an anonymous request an
	// auth challenge.
	challenged atomic.Bool

	mu      sync.Mutex
	pullers []*puller // most recently used first
	max     int       // of repositories remembered across pullers
}

// puller is the remote.Puller kept for one identity. It remembers how the
// registry wants to be signed in to and the identity's tokens, repository by
// repository, so only the first call for a repository has to ask.
type puller struct {
	*remote.Puller
	id    string              // as identity gives it, so no password is held as a key
	repos map[string]struct{} // the names it has been asked about; guarded by API.mu
}

var _ Deleter = (*API)(nil)

// Option configures an API made by NewAPI.
type Option func(*apiOptions)

type apiOptions struct {
	header http.Header
	cert   *tls.Certificate
}

// WithHeader sends header with every request to the registry's host. It is
// not sent to other hosts, such as a token service or a blob redirect.
func WithHeader(header http.Header) Option {
	return func(o *apiOptions) { o.header = header }
}

// WithClientCert presents cert when a server asks for a client certificate
// (mutual TLS). A nil cert presents none.
func WithClientCert(cert *tls.Certificate) Option {
	return func(o *apiOptions) { o.cert = cert }
}

// NewAPI returns a Source for the registry at registryURL, e.g.
// "http://registry:5000". insecure skips TLS certificate verification.
func NewAPI(registryURL string, insecure bool, opts ...Option) (*API, error) {
	u, err := url.Parse(registryURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Trim(u.Path, "/") != "" {
		return nil, fmt.Errorf("registry URL %q: want http(s)://host[:port]", registryURL)
	}
	var o apiOptions
	for _, opt := range opts {
		opt(&o)
	}
	var nameOpts []name.Option
	if u.Scheme == "http" {
		nameOpts = append(nameOpts, name.Insecure)
	}
	a := &API{max: maxRemembered}
	if a.reg, err = name.NewRegistry(u.Host, nameOpts...); err != nil {
		return nil, fmt.Errorf("registry URL %q: %w", registryURL, err)
	}
	t := remote.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecure}
	if o.cert != nil {
		t.TLSClientConfig.Certificates = []tls.Certificate{*o.cert}
	}
	a.transport = roundTripper{inner: t, host: a.reg.RegistryStr(), header: o.header}
	return a, nil
}

// Catalog implements Source.
func (a *API) Catalog(ctx context.Context) ([]string, error) {
	ctx, c := a.call(ctx)
	p, err := a.puller(ctx, c, "")
	if err != nil {
		return nil, err
	}
	repos, err := p.Catalog(ctx, a.reg)
	if err != nil {
		return nil, c.fail("catalog", err)
	}
	slices.Sort(repos)
	return repos, nil
}

// Check asks the registry whether it accepts the credentials in ctx, with a
// GET /v2/ after any token exchange the registry asks for. It is never
// cached and uses no kept puller, so login can use it. A rejection is an
// *UnauthorizedError.
func (a *API) Check(ctx context.Context) error {
	ctx, c := a.call(ctx)
	t, err := transport.NewWithContext(ctx, a.reg, c.auth, a.transport, nil)
	if err != nil {
		return c.fail("check", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.reg.Scheme()+"://"+a.reg.RegistryStr()+"/v2/", nil)
	if err != nil {
		return err
	}
	resp, err := t.RoundTrip(req)
	if err != nil {
		return c.fail("check", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := transport.CheckError(resp, http.StatusOK); err != nil {
		return c.fail("check", err)
	}
	return nil
}

// Challenged reports whether the registry has sent an auth challenge to an
// anonymous request since a was made, on /v2/ or anywhere else, so signing
// in might show more.
func (a *API) Challenged() bool { return a.challenged.Load() }

// Tags implements Source. A 404 reads as no tags: Distribution answers it
// for a repository it still lists in the catalog but that has no tags
// folder, such as one only ever pushed by digest. That can't be told apart
// from a name that isn't in the catalog, so an unknown repository has no
// tags rather than not being found.
func (a *API) Tags(ctx context.Context, repo string) ([]string, error) {
	r, err := a.repo(repo)
	if err != nil {
		return nil, err
	}
	ctx, c := a.call(ctx)
	p, err := a.puller(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	tags, err := p.List(ctx, r)
	if err != nil {
		if err = c.fail("tags of "+repo, err); errors.Is(err, ErrNotFound) {
			return []string{}, nil
		}
		return nil, err
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

	ctx, c := a.call(ctx)
	p, err := a.puller(ctx, c, repo)
	if err != nil {
		return Manifest{}, err
	}
	m, err := resolve(ctx, p, r, target)
	if err != nil {
		return Manifest{}, c.fail("manifest "+repo+":"+ref, err)
	}
	return m, nil
}

// Digest implements Source with a HEAD request, which Docker Hub doesn't
// count against its pull limit. A registry whose HEAD answer leaves out the
// digest or the size is asked with a GET instead.
func (a *API) Digest(ctx context.Context, repo, tag string) (string, error) {
	r, err := a.repo(repo)
	if err != nil {
		return "", err
	}
	if !tagName.MatchString(tag) {
		return "", fmt.Errorf("manifest %s:%s: %w", repo, tag, ErrNotFound)
	}
	ctx, c := a.call(ctx)
	p, err := a.puller(ctx, c, repo)
	if err != nil {
		return "", err
	}
	desc, err := p.Head(ctx, r.Tag(tag))
	if te := (*transport.Error)(nil); err != nil && !errors.As(err, &te) {
		var got *remote.Descriptor
		if got, err = p.Get(ctx, r.Tag(tag)); err == nil {
			desc = &got.Descriptor
		}
	}
	if err != nil {
		return "", c.fail("manifest "+repo+":"+tag, err)
	}
	return desc.Digest.String(), nil
}

func resolve(ctx context.Context, p *remote.Puller, repo name.Repository, ref name.Reference) (Manifest, error) {
	desc, err := p.Get(ctx, ref)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Digest: desc.Digest.String(), MediaType: string(desc.MediaType)}
	// The cache's last answer for ref has expired, since a tag can move. What
	// it says of a digest still holds, so that much isn't fetched again.
	by, _ := ctx.Value(askedKey{}).(asked)
	last, _ := by.last.(Manifest)
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
			wg.Go(func() { m.Platforms[i], errs[i] = platform(ctx, p, repo, d, idx.Annotations, last) })
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return Manifest{}, err
		}
		return m, nil
	}

	if last.Digest == m.Digest {
		return last, nil // the tag hasn't moved
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
	if by.cache != nil {
		// The page asks for this config next.
		by.cache.keepConfig(ctx, repo.RepositoryStr(), mf.Config.Digest.String(), shown(cfg))
	}
	m.Platforms = []Platform{{
		OS: cfg.OS, Arch: cfg.Architecture, Variant: cfg.Variant,
		Digest: m.Digest, MediaType: m.MediaType, Config: mf.Config.Digest.String(), Size: m.Size,
		Created: annotated(mf.Annotations),
	}}
	return m, nil
}

// platform summarises one image listed in an index. One the registry
// doesn't have (404) is marked missing; any other error fails. One that
// last, the answer before this one, already summarises isn't fetched again,
// unless it was missing then: a mirror may have fetched it since.
func platform(ctx context.Context, p *remote.Puller, repo name.Repository, d v1.Descriptor, index map[string]string, last Manifest) (Platform, error) {
	pl := indexed(d)
	if i := slices.IndexFunc(last.Platforms, func(was Platform) bool { return was.Digest == pl.Digest && !was.Missing }); i >= 0 {
		pl.Config, pl.Size, pl.Created = last.Platforms[i].Config, last.Platforms[i].Size, last.Platforms[i].Created
		return pl, nil
	}
	desc, err := p.Get(ctx, repo.Digest(d.Digest.String()))
	if te := (*transport.Error)(nil); errors.As(err, &te) && te.StatusCode == http.StatusNotFound {
		pl.Missing = true
		return pl, nil
	}
	if err != nil {
		return Platform{}, err
	}
	mf, err := v1.ParseManifest(bytes.NewReader(desc.Manifest))
	if err != nil {
		return Platform{}, err
	}
	pl.Config, pl.Size, pl.Created = mf.Config.Digest.String(), size(mf), annotated(mf.Annotations, index)
	return pl, nil
}

// indexed is what an index's entry says about a platform.
func indexed(d v1.Descriptor) Platform {
	pl := Platform{Digest: d.Digest.String(), MediaType: string(d.MediaType)}
	if d.Platform != nil {
		pl.OS, pl.Arch, pl.Variant = d.Platform.OS, d.Platform.Architecture, d.Platform.Variant
	}
	return pl
}

// Config implements Source.
func (a *API) Config(ctx context.Context, repo, digest string) (ImageConfig, error) {
	r, err := a.repo(repo)
	if err != nil {
		return ImageConfig{}, err
	}
	ctx, c := a.call(ctx)
	p, err := a.puller(ctx, c, repo)
	if err != nil {
		return ImageConfig{}, err
	}
	cfg, err := config(ctx, p, r, digest)
	if err != nil {
		return ImageConfig{}, c.fail("config "+repo+"@"+digest, err)
	}
	return shown(cfg), nil
}

// shown is the part of a config that Config answers with.
// Without a created date it falls back to the OCI created label.
func shown(cfg *v1.ConfigFile) ImageConfig {
	created := cfg.Created.Time
	if created.IsZero() {
		created = annotated(cfg.Config.Labels)
	}
	return ImageConfig{Created: created, Labels: cfg.Config.Labels}
}

// history is what History answers for a manifest and its config.
func history(mf *v1.Manifest, cfg *v1.ConfigFile) History {
	h := History{Layers: make([]Layer, len(mf.Layers)), Labels: cfg.Config.Labels}
	for i, l := range mf.Layers {
		h.Layers[i] = Layer{Digest: l.Digest.String(), Size: l.Size}
	}
	for _, s := range cfg.History {
		h.Steps = append(h.Steps, Step{Created: s.Created.Time, CreatedBy: s.CreatedBy, Empty: s.EmptyLayer})
	}
	return h
}

// annotated is the first RFC 3339 org.opencontainers.image.created among
// labels or annotations, or zero if none parses.
func annotated(maps ...map[string]string) time.Time {
	for _, m := range maps {
		if t, err := time.Parse(time.RFC3339, m["org.opencontainers.image.created"]); err == nil {
			return t
		}
	}
	return time.Time{}
}

// TagTime implements Source. The registry API records no push time.
func (a *API) TagTime(context.Context, string, string) (time.Time, error) {
	return time.Time{}, errors.ErrUnsupported
}

// DiskUsage implements Source. Only every manifest in the registry would
// tell, which is too many requests.
func (a *API) DiskUsage(context.Context, string) (int64, error) {
	return 0, errors.ErrUnsupported
}

// History implements Source.
func (a *API) History(ctx context.Context, repo, digest string) (History, error) {
	r, err := a.repo(repo)
	if err != nil {
		return History{}, err
	}
	if !digestRef.MatchString(digest) {
		return History{}, fmt.Errorf("manifest %s@%s: %w", repo, digest, ErrNotFound)
	}
	ctx, c := a.call(ctx)
	p, err := a.puller(ctx, c, repo)
	if err != nil {
		return History{}, err
	}
	h, err := func() (History, error) {
		desc, err := p.Get(ctx, r.Digest(digest))
		if err != nil {
			return History{}, err
		}
		if !desc.MediaType.IsImage() {
			return History{}, ErrNotFound
		}
		mf, err := v1.ParseManifest(bytes.NewReader(desc.Manifest))
		if err != nil {
			return History{}, err
		}
		cfg, err := config(ctx, p, r, mf.Config.Digest.String())
		if err != nil {
			return History{}, err
		}
		return history(mf, cfg), nil
	}()
	if err != nil {
		return History{}, c.fail("history "+repo+"@"+digest, err)
	}
	return h, nil
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

// call is one Source call's view of the registry: who it signs in as, the
// puller it uses, and whether the registry has sent it an auth challenge.
type call struct {
	api        *API
	auth       authn.Authenticator
	puller     *puller // nil for Check
	challenged atomic.Bool
	anon       *atomic.Bool // the API's challenged, for anonymous calls only
}

type callKey struct{}

// call starts a call. Its requests must be made with the context it returns,
// which is how the transport finds the call they belong to.
func (a *API) call(ctx context.Context) (context.Context, *call) {
	c := &call{api: a, auth: authn.Anonymous, anon: &a.challenged}
	if cr, ok := ctx.Value(credentialsKey{}).(credentials); ok {
		c.auth, c.anon = &authn.Basic{Username: cr.user, Password: cr.pass}, nil
	}
	return context.WithValue(ctx, callKey{}, c), c
}

// puller returns the puller kept for ctx's identity, making one if there is
// none, for a call about repo ("" for the catalog). Each identity has its
// own, so what one user signed in for is never used for another user or an
// anonymous visitor.
func (a *API) puller(ctx context.Context, c *call, repo string) (*remote.Puller, error) {
	id := identity(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	// shortcut: a scan, fine for the handful of people signed in at once;
	// index by id if a registry ever has thousands.
	i := slices.IndexFunc(a.pullers, func(p *puller) bool { return p.id == id })
	if i < 0 {
		rp, err := remote.NewPuller(
			remote.WithAuth(signIn{}),
			remote.WithTransport(a.transport),
			// Fail fast: a page waiting on retries is worse than an error page.
			remote.WithRetryStatusCodes(),
			remote.WithRetryPredicate(func(error) bool { return false }),
			// A puller reads four blobs at a time unless told otherwise. This
			// one serves every page at once, and pages limit themselves.
			remote.WithJobs(1<<20),
		)
		if err != nil {
			return nil, err
		}
		i = len(a.pullers)
		a.pullers = append(a.pullers, &puller{Puller: rp, id: id, repos: map[string]struct{}{}})
	}
	p := a.pullers[i]
	a.pullers = slices.Insert(slices.Delete(a.pullers, i, i+1), 0, p)
	p.repos[repo] = struct{}{}
	// Drop the least recently used once there is too much to remember. That
	// can be p itself, which then serves only this call.
	n := 0
	for i, kept := range a.pullers {
		if n += len(kept.repos); n > a.max {
			a.pullers = slices.Delete(a.pullers, i, len(a.pullers))
			break
		}
	}
	c.puller = p
	return p.Puller, nil
}

// signIn is the Authenticator pullers are made with. It signs each request
// in as the call it belongs to, so a kept puller holds the tokens the
// registry gave it but never a password.
type signIn struct{}

// Authorization implements authn.Authenticator, for a request without a call.
func (signIn) Authorization() (*authn.AuthConfig, error) { return authn.Anonymous.Authorization() }

// AuthorizationContext implements authn.ContextAuthenticator.
func (s signIn) AuthorizationContext(ctx context.Context) (*authn.AuthConfig, error) {
	if c, ok := ctx.Value(callKey{}).(*call); ok {
		return c.auth.Authorization()
	}
	return s.Authorization()
}

// retire stops p being reused. Calls already using it finish with it.
func (a *API) retire(p *puller) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i := slices.Index(a.pullers, p); i >= 0 {
		a.pullers = slices.Delete(a.pullers, i, i+1)
	}
}

// roundTripper bounds each request to requestTimeout, adds header to
// requests for host and notes auth challenges. Pullers keep it from one call
// to the next, so it finds the call a request belongs to in the request's
// context.
type roundTripper struct {
	inner  http.RoundTripper
	host   string
	header http.Header
}

// RoundTrip implements http.RoundTripper.
func (t roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), requestTimeout)
	req = req.WithContext(ctx)
	if len(t.header) > 0 && req.URL.Host == t.host {
		// A RoundTripper mustn't change the caller's request, and WithContext
		// shares its header map.
		req.Header = req.Header.Clone()
		maps.Copy(req.Header, t.header)
	}
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if c, ok := ctx.Value(callKey{}).(*call); ok && resp.StatusCode == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") != "" {
		c.challenged.Store(true)
		if c.anon != nil {
			c.anon.Store(true)
		}
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
			// The registry may have changed how it wants to be signed in to,
			// so the identity's next call asks it again, as every call used to.
			c.api.retire(c.puller)
			return &UnauthorizedError{Challenged: c.challenged.Load()}
		case http.StatusForbidden:
			return fmt.Errorf("%s: %w", what, ErrForbidden)
		case http.StatusMethodNotAllowed:
			return fmt.Errorf("%s: %w", what, ErrDeleteDisabled)
		}
	}
	return fmt.Errorf("%s: %w: %w", what, ErrUnavailable, err)
}
