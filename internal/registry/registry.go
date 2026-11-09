// Package registry defines how the UI reads a registry's contents. Both the
// API client and the filesystem storage reader implement Source, so pages
// never know which one they are talking to.
package registry

import (
	"context"
	"errors"
	"time"
)

// Source reads repositories, tags, manifests and image configs from a
// registry. Implementations must be safe for concurrent use.
type Source interface {
	// Catalog returns every repository name, sorted A–Z.
	Catalog(ctx context.Context) ([]string, error)

	// Tags returns the tag names of repo in no particular order.
	Tags(ctx context.Context, repo string) ([]string, error)

	// Resolve looks up a manifest by tag or by digest ("sha256:…").
	// Attestation manifests inside an index are left out of Platforms.
	Resolve(ctx context.Context, repo, ref string) (Manifest, error)

	// Config returns the image config with the given digest, usually a
	// Platform.Config value.
	Config(ctx context.Context, repo, digest string) (ImageConfig, error)

	// TagTime returns when tag last changed on this registry: Distribution
	// rewrites a tag's link on every push of it and, on a pull-through
	// cache, on every pull by tag. It returns errors.ErrUnsupported when
	// the source can't tell, as the registry API can't.
	TagTime(ctx context.Context, repo, tag string) (time.Time, error)

	// DiskUsage returns the bytes repo's blobs take up on disk, or the whole
	// registry's when repo is "". Layers are stored once however many
	// repositories link to them, so a shared blob counts in each repository
	// but once in the registry. It returns errors.ErrUnsupported when the
	// source can't tell, as the registry API can't.
	DiskUsage(ctx context.Context, repo string) (int64, error)
}

// Kind says what a manifest holds, so pages can pick how to show it.
type Kind int

const (
	// KindImage is a single-platform container image.
	KindImage Kind = iota
	// KindIndex is a multi-platform image (OCI index or Docker manifest list).
	KindIndex
	// KindSignature is a cosign-style signature, attestation or SBOM tag.
	KindSignature
	// KindHelm is a Helm chart stored as an OCI artifact.
	KindHelm
	// KindArtifact is any other non-image OCI artifact.
	KindArtifact
)

// Manifest is a parsed summary of a manifest or index, not the raw document.
type Manifest struct {
	// Digest of the manifest itself, e.g. "sha256:…".
	Digest string
	// MediaType of the manifest, e.g. "application/vnd.oci.image.index.v1+json".
	MediaType string
	Kind      Kind
	// Size is the compressed size: the config plus the layers. Zero for an
	// index; use the per-platform sizes instead.
	Size int64
	// Platforms lists the runnable images. An index has one entry per
	// platform, in the index's order; a single-platform image has one entry
	// whose Digest equals the manifest's. Signatures and other artifacts
	// have none. A platform the index lists but the registry doesn't hold
	// is still there, marked Missing.
	Platforms []Platform
}

// Platform is one runnable image within a manifest.
type Platform struct {
	OS      string
	Arch    string
	Variant string
	// Digest of this platform's image manifest.
	Digest string
	// MediaType of this platform's image manifest, as listed in the index.
	MediaType string
	// Config is the digest of this platform's image config; pass it to
	// Source.Config.
	Config string
	// Size is the compressed size: the config plus the layers.
	Size int64
	// Missing means the index lists this platform but its manifest isn't in
	// the registry: a mirror never fetched it, or garbage collection removed
	// it. Only what the index says is set: OS, Arch, Variant, Digest and
	// MediaType. There's no Config or Size.
	Missing bool
}

type credentialsKey struct{}

type credentials struct{ user, pass string }

// WithCredentials returns a copy of ctx whose registry requests sign in as
// user. Requests made without it are anonymous.
func WithCredentials(ctx context.Context, user, pass string) context.Context {
	return context.WithValue(ctx, credentialsKey{}, credentials{user, pass})
}

// ImageConfig holds the parts of an image config the UI shows.
type ImageConfig struct {
	// Created is the image's build time. Registries record no push time.
	Created time.Time
	Labels  map[string]string
}

// Errors a Source returns, wrapped or as-is, so the web layer can pick the
// right page with errors.Is.
var (
	// ErrNotFound means the repository, tag, manifest or blob doesn't exist.
	ErrNotFound = errors.New("not found")
	// ErrUnauthorized means the registry wants credentials. The concrete
	// error is an *UnauthorizedError.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden means the credentials (or anonymous access) aren't
	// allowed to do this, e.g. list the catalog.
	ErrForbidden = errors.New("forbidden")
	// ErrUnavailable means the registry couldn't be reached or failed.
	ErrUnavailable = errors.New("registry unavailable")
)

// UnauthorizedError is returned when the registry answers 401. It matches
// ErrUnauthorized with errors.Is.
type UnauthorizedError struct {
	// Challenged is true when the registry sent a WWW-Authenticate challenge,
	// meaning signing in could help.
	Challenged bool
}

func (e *UnauthorizedError) Error() string { return ErrUnauthorized.Error() }

// Is makes errors.Is(err, ErrUnauthorized) true.
func (e *UnauthorizedError) Is(target error) bool { return target == ErrUnauthorized }
