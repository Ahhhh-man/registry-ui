package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// sameAsLimit caps the tags resolved to find "Same as" tags, the same
// bound as the repo page's 100 tags per page.
const sameAsLimit = 100

// tagView is the data for the "tag" and "tag-not-found" templates.
type tagView struct {
	Repo, Namespace, Name string // "library/nginx", "library", "nginx"
	Tag                   string // "" on a digest page
	Prefix, Title         string // "nginx:" and "1.27.3", or "nginx@" and "sha256:3f2a9c1b7e44"
	Kind                  string // "image", "signature", "helm" or "artifact"
	Manifest              registry.Manifest
	Badges, Pills         []map[string]any // page-header data
	Pull, PullTitle       string           // header pull command; "" for none

	// Images only: the platform list and the selected platform's details.
	Platforms         []platformLink
	Platform          registry.Platform
	PlatformMediaType string
	PlatformCreated   time.Time
	PlatformPull      string
}

// platformLink is an entry in the "platform-list" partial.
type platformLink struct {
	Label, Size string // "linux/arm64", "67.0 MB"
	Href        string // this page with ?platform=
	Current     bool
}

// Tag serves a tag's detail page (#14).
func (s *Server) Tag(w http.ResponseWriter, r *http.Request, ref web.Ref) {
	s.manifest(w, r, ref.Repo, ref.Tag, ref.Tag)
}

// Digest serves a manifest by digest, on the same page as Tag (#14).
func (s *Server) Digest(w http.ResponseWriter, r *http.Request, ref web.Ref) {
	s.manifest(w, r, ref.Repo, "", ref.Digest)
}

// manifest renders the page for repo at ref, a tag or a digest. tag is ""
// for a digest. With ?fragment=platform (htmx) it renders only the
// platform list and panel, for ?platform= switches.
func (s *Server) manifest(w http.ResponseWriter, r *http.Request, repo, tag, ref string) {
	ctx := r.Context()
	v := tagView{Repo: repo, Tag: tag, Name: repo, Title: tag}
	if i := strings.LastIndexByte(repo, '/'); i >= 0 {
		v.Namespace, v.Name = repo[:i], repo[i+1:]
	}
	sep := ":"
	if tag == "" {
		sep, v.Title = "@", "sha256:"+format.ShortDigest(ref)
	}
	v.Prefix = v.Name + sep
	pageTitle := repo + sep + v.Title

	m, err := s.Src.Resolve(ctx, repo, ref)
	if errors.Is(err, registry.ErrNotFound) && web.Fragment(r) == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		s.page(w, r, ui.Page{PageTitle: pageTitle}, "tag-not-found", v)
		return
	}
	if err != nil {
		web.Error(w, r, err)
		return
	}
	v.Manifest = m

	host := s.PullHost + "/" + repo
	pinned := host + "@" + m.Digest
	if tag != "" {
		pinned = host + ":" + tag
	}
	switch m.Kind {
	case registry.KindImage, registry.KindIndex:
		if len(m.Platforms) == 0 { // only attestations, say
			v.Kind = "artifact"
			break
		}
		v.Kind, v.Pull, v.PullTitle = "image", "docker pull "+pinned, "Pull this image"
	case registry.KindSignature:
		v.Kind = "signature"
		// "sha256-<64 hex>.sig" becomes "sha256-<12 hex>….sig".
		if i := strings.LastIndexByte(tag, '.'); strings.HasPrefix(tag, "sha256-") && i > 19 {
			v.Title = tag[:19] + "…" + tag[i:]
		}
	case registry.KindHelm:
		v.Kind, v.PullTitle = "helm", "Pull this chart"
		v.Pull = "helm pull oci://" + host + "@" + m.Digest
		if tag != "" {
			v.Pull = "helm pull oci://" + host + " --version " + tag
		}
	default:
		v.Kind = "artifact"
	}

	var created time.Time // the default platform's, for the header
	if v.Kind == "image" {
		if err := s.platforms(ctx, r, &v, &created); err != nil {
			web.Error(w, r, err)
			return
		}
	}
	if web.Fragment(r) == "platform" && v.Kind == "image" {
		s.fragment(w, r, "tag-platforms", v)
		return
	}

	if tag == "latest" {
		v.Badges = append(v.Badges, map[string]any{"Label": "latest", "Variant": "accent"})
	}
	switch v.Kind {
	case "signature":
		v.Badges = append(v.Badges, map[string]any{"Label": "signature", "Variant": "signature"})
	case "helm":
		v.Badges = append(v.Badges, map[string]any{"Label": "helm chart", "Variant": "helm"})
	case "artifact":
		v.Badges = append(v.Badges, map[string]any{"Label": "artifact"})
	}

	if !created.IsZero() {
		v.Pills = append(v.Pills, map[string]any{"Icon": "clock", "Text": "Created " + format.Age(created) + " · " + format.Date(created)})
	}
	v.Pills = append(v.Pills, map[string]any{"Icon": "hash", "Text": format.ShortDigest(m.Digest), "Mono": true, "Copy": m.Digest})
	if n := len(m.Platforms); n > 0 {
		v.Pills = append(v.Pills, map[string]any{"Icon": "cpu", "Text": plural(n, "platform")})
	}
	if same := s.sameAs(ctx, repo, tag, m.Digest); len(same) > 0 {
		v.Pills = append(v.Pills, map[string]any{"Icon": "link", "Text": "Same as " + strings.Join(same, ", ")})
	}

	s.page(w, r, ui.Page{PageTitle: pageTitle}, "tag", v)
}

// platforms fills in v's platform list and the selected platform's details:
// ?platform= if it names one, else linux/amd64, else the first. created
// gets the default platform's created time. A config or platform manifest
// the registry lacks (a pull-through cache holds only what was pulled)
// leaves its fields empty rather than failing the page.
func (s *Server) platforms(ctx context.Context, r *http.Request, v *tagView, created *time.Time) error {
	m := v.Manifest
	def := 0
	for i, p := range m.Platforms {
		if p.OS == "linux" && p.Arch == "amd64" {
			def = i
			break
		}
	}
	sel, want := def, r.URL.Query().Get("platform")
	for i, p := range m.Platforms {
		name := format.Platform(p)
		if name == want {
			sel = i
		}
		href := r.URL.Path + "?platform=" + strings.ReplaceAll(url.QueryEscape(name), "%2F", "/")
		v.Platforms = append(v.Platforms, platformLink{Label: name, Size: format.Size(p.Size), Href: href})
	}
	v.Platforms[sel].Current = true
	v.Platform = m.Platforms[sel]
	v.PlatformPull = "docker pull " + s.PullHost + "/" + v.Repo + "@" + v.Platform.Digest

	config := func(p registry.Platform) (time.Time, error) {
		c, err := s.Src.Config(ctx, v.Repo, p.Config)
		if errors.Is(err, registry.ErrNotFound) {
			err = nil
		}
		return c.Created, err
	}
	var err error
	if v.PlatformCreated, err = config(v.Platform); err != nil {
		return err
	}
	*created = v.PlatformCreated
	if sel != def {
		if *created, err = config(m.Platforms[def]); err != nil {
			return err
		}
	}

	v.PlatformMediaType = m.MediaType
	if m.Kind == registry.KindIndex {
		pm, err := s.Src.Resolve(ctx, v.Repo, v.Platform.Digest)
		if err != nil && !errors.Is(err, registry.ErrNotFound) {
			return err
		}
		v.PlatformMediaType = pm.MediaType
	}
	return nil
}

// sameAs returns the other tags in repo that point at digest, newest first.
// It's best effort: a tag that fails to resolve is left out.
// shortcut: repos with more than sameAsLimit tags get no "Same as", so the
// page never waits on more resolves; lazy-load it if big repos need it.
func (s *Server) sameAs(ctx context.Context, repo, tag, digest string) []string {
	tags, err := s.Src.Tags(ctx, repo)
	if err != nil || len(tags) > sameAsLimit {
		return nil
	}
	var (
		mu   sync.Mutex
		same []string
		g    errgroup.Group
	)
	g.SetLimit(8)
	for _, t := range tags {
		if t == tag {
			continue
		}
		g.Go(func() error {
			if m, err := s.Src.Resolve(ctx, repo, t); err == nil && m.Digest == digest {
				mu.Lock()
				same = append(same, t)
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait() // the goroutines never fail
	format.SortTags(same)
	return same
}
