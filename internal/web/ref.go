package web

import (
	"regexp"
	"strings"
)

// Kind says which page a path addresses.
type Kind int

const (
	// KindCatalog is the repository list at the base path.
	KindCatalog Kind = iota
	// KindRepo is a repository, e.g. /library/nginx.
	KindRepo
	// KindTag is a tag of a repository, e.g. /library/nginx:1.27.3.
	KindTag
	// KindDigest is a manifest by digest, e.g. /library/nginx@sha256:….
	KindDigest
)

// Ref is a parsed page path. Repo, Tag and Digest are set as Kind needs.
type Ref struct {
	Kind   Kind
	Repo   string
	Tag    string
	Digest string
}

// Grammar from the distribution reference package. Its separator also allows
// "__" and runs of "-", which the simpler [._-] would wrongly reject.
var (
	nameRe   = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagRe    = regexp.MustCompile(`^\w[\w.-]{0,127}$`)
	digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// parseRef parses a path below the base path, without its leading slash,
// e.g. "library/nginx:1.27.3". It reports false for anything invalid.
func parseRef(p string) (Ref, bool) {
	if p == "" {
		return Ref{Kind: KindCatalog}, true
	}
	if repo, digest, ok := strings.Cut(p, "@"); ok {
		return Ref{Kind: KindDigest, Repo: repo, Digest: digest},
			nameRe.MatchString(repo) && digestRe.MatchString(digest)
	}
	// The tag follows the last ":" after the last "/".
	if i := strings.LastIndexByte(p, ':'); i > strings.LastIndexByte(p, '/') {
		repo, tag := p[:i], p[i+1:]
		return Ref{Kind: KindTag, Repo: repo, Tag: tag},
			nameRe.MatchString(repo) && tagRe.MatchString(tag)
	}
	return Ref{Kind: KindRepo, Repo: p}, nameRe.MatchString(p)
}
