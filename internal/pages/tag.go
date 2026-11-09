package pages

import (
	"cmp"
	"context"
	"errors"
	"net/http"
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
// bound as the repo page's default tags per page.
const sameAsLimit = 100

// tagView is the data for the "tag", "tag-band" and "tag-not-found" templates.
type tagView struct {
	Repo, Namespace, Name string // "library/nginx", "library", "nginx"
	Tag                   string // "" on a digest page
	Prefix, Title         string // "nginx:" and "1.27.3", or "nginx@" and "sha256:3f2a9c1b7e44"
	Kind                  string // "image", "signature", "helm" or "artifact"
	Manifest              registry.Manifest
	Index                 bool             // Manifest is a multi-platform index
	Badges                []map[string]any // page-header data
	Pills                 []pill
	Pull                  string // header pull command; "" for none
	Ref                   string // "registry.example.com/library/nginx:1.27.3", or "…@sha256:…" on a digest page
	// Cards are "fact-card" data: one per platform of an image, or one
	// for a chart, signature or artifact. An image with none has nothing
	// in storage yet.
	Cards   []map[string]any
	Created string // the main platform's created time in UTC; "" if unknown
	Missing bool   // for "tag-not-found": the tag is listed, but its manifest is missing
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
// for a digest. ?platform=os/arch highlights that platform's card.
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
	if errors.Is(err, registry.ErrNotFound) {
		if tag != "" {
			tags, _ := s.Src.Tags(ctx, repo) // on error, it's just not found
			v.Missing = manifestMissing(err, tags, tag)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		s.page(w, r, ui.Page{PageTitle: pageTitle}, "tag-not-found", v)
		return
	}
	if err != nil {
		web.Error(w, r, err)
		return
	}
	v.Manifest, v.Index = m, m.Kind == registry.KindIndex

	host := s.PullHost + "/" + repo
	pinned := host + "@" + m.Digest
	if tag != "" {
		pinned = host + ":" + tag
	}
	v.Ref = pinned
	digest := map[string]any{"Label": "Digest", "Value": format.ShortDigest(m.Digest), "Mono": true, "Title": m.Digest}
	copyDigest := []map[string]any{{"Text": m.Digest, "Caption": "Copy digest"}}
	// A kind with no pull command has no pull box, so its card copies the reference.
	copyRef := append([]map[string]any{{"Text": pinned, "Caption": "Copy reference"}}, copyDigest...)
	var created time.Time // the main platform's, for the header
	switch m.Kind {
	case registry.KindImage, registry.KindIndex:
		v.Kind, v.Pull = "image", "docker pull "+pinned
		if created, err = s.platformCards(ctx, r, &v); err != nil {
			web.Error(w, r, err)
			return
		}
	case registry.KindSignature:
		v.Kind = "signature"
		facts := []map[string]any{digest}
		// A cosign tag, "sha256-<64 hex>.sig", names the image it belongs to.
		if sm := sigTag.FindStringSubmatch(tag); sm != nil {
			v.Title = tagLabel(tag)
			target := "sha256:" + sm[1]
			facts = append([]map[string]any{{"Label": "Belongs to", "Value": "sha256:" + format.ShortDigest(target), "Mono": true, "Title": target, "Href": s.BasePath + repo + "@" + target}}, facts...)
		}
		v.Cards = []map[string]any{{
			"Icon": "shield-check", "Title": "Signature", "Size": format.Size(m.Size), "Facts": facts, "Copies": copyRef,
			"Text": "A cosign signature, attestation or SBOM for an image in this repository. It isn't an image you can run.",
		}}
	case registry.KindHelm:
		v.Kind = "helm"
		v.Pull = "helm pull oci://" + host + "@" + m.Digest
		if tag != "" {
			v.Pull = "helm pull oci://" + host + " --version " + tag
		}
		v.Cards = []map[string]any{{
			"Icon": "anchor", "Title": "Helm chart", "Size": format.Size(m.Size), "Copies": copyDigest,
			"Facts": []map[string]any{digest, {"Label": "Type", "Value": "OCI artifact"}},
		}}
	default:
		v.Kind = "artifact"
		v.Cards = []map[string]any{{
			"Icon": "package", "Title": "OCI artifact", "Size": format.Size(m.Size), "Copies": copyRef,
			"Text":  "This isn't a container image, so there are no platforms to show.",
			"Facts": []map[string]any{digest, {"Label": "Type", "Value": manifestType(m.MediaType)}},
		}}
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
		v.Created = created.UTC().Format("2006-01-02 15:04:05 UTC")
		v.Pills = append(v.Pills, pill{Icon: "clock", Text: "Created ", Time: created})
	}
	if tag != "" {
		if t, err := s.Src.TagTime(ctx, repo, tag); err == nil {
			v.Pills = append(v.Pills, pill{Icon: "history", Text: "Updated ", Time: t, Title: "{}: when this tag was last pushed or, on a pull-through cache, last pulled"})
		}
	}
	if len(m.Platforms) > 0 {
		text := platformCount(m.Platforms)
		if p, ok := mainPlatform(m); ok {
			text += " · " + format.Size(p.Size) + " on " + format.ShortPlatform(p)
		}
		v.Pills = append(v.Pills, pill{Icon: "cpu", Text: text})
	}
	if same := s.sameAs(ctx, repo, tag, m.Digest); len(same) > 0 {
		links := make([]link, len(same))
		for i, t := range same {
			links[i] = link{t, s.BasePath + repo + ":" + t}
		}
		v.Pills = append(v.Pills, pill{Icon: "link", Text: "Same as", Links: links})
	}

	s.page(w, r, ui.Page{PageTitle: pageTitle}, "tag", v)
}

// platformCards adds a card per platform to v, fetching each platform's
// config for its created time, and returns the main platform's. A config
// the registry lacks (a pull-through cache holds only what was pulled)
// leaves it unknown rather than failing the page. Missing platforms come
// last, dimmed, with a note on why they're missing and no pull command
// or reference.
func (s *Server) platformCards(ctx context.Context, r *http.Request, v *tagView) (time.Time, error) {
	ps := availableFirst(v.Manifest.Platforms)
	if len(ps) == 0 {
		return time.Time{}, nil
	}
	created := make([]time.Time, len(ps))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, p := range ps {
		if p.Missing {
			continue
		}
		g.Go(func() error {
			c, err := s.Src.Config(gctx, v.Repo, p.Config)
			if errors.Is(err, registry.ErrNotFound) {
				return nil
			}
			created[i] = c.Created
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return time.Time{}, err
	}

	var main time.Time
	want := r.URL.Query().Get("platform")
	mp, ok := mainPlatform(v.Manifest)
	note := "This platform's manifest isn't in the registry. A mirror may not have fetched it, or garbage collection may have removed it."
	if s.Storage {
		note = "Not cached. Nobody has pulled this platform through the cache yet."
	}
	for i, p := range ps {
		if ok && p.Digest == mp.Digest {
			main = created[i]
		}
		name := format.Platform(p)
		id := "platform-" + strings.ReplaceAll(name, "/", "-")
		if p.Missing {
			v.Cards = append(v.Cards, map[string]any{
				"ID": id, "Current": name == want, "Missing": true, "Icon": "cpu", "Title": name, "Text": note,
				"Facts": []map[string]any{
					{"Label": "Digest", "Value": format.ShortDigest(p.Digest), "Mono": true, "Title": p.Digest},
					{"Label": "Type", "Value": manifestType(p.MediaType)},
				},
				"Copies": []map[string]any{{"Text": p.Digest, "Caption": "Copy digest", "Label": "Copy digest of " + name}},
			})
			continue
		}
		v.Cards = append(v.Cards, map[string]any{
			"ID": id, "Current": name == want,
			"Icon": "cpu", "Title": name, "Size": format.Size(p.Size),
			"Facts": []map[string]any{
				{"Label": "Digest", "Value": format.ShortDigest(p.Digest), "Mono": true, "Title": p.Digest, "Href": s.BasePath + v.Repo + "@" + p.Digest},
				createdFact(created[i]),
				{"Label": "Type", "Value": manifestType(p.MediaType)},
			},
			"Copies": []map[string]any{
				{"Text": "docker pull " + s.PullHost + "/" + v.Repo + "@" + p.Digest, "Caption": "Copy pull command", "Label": "Copy pull command for " + name},
				{"Text": s.PullHost + "/" + v.Repo + "@" + p.Digest, "Caption": "Copy reference", "Label": "Copy reference for " + name},
				{"Text": p.Digest, "Caption": "Copy digest", "Label": "Copy digest of " + name},
			},
		})
	}
	return main, nil
}

// manifestType names an image manifest's media type for people.
func manifestType(mediaType string) string {
	switch mediaType {
	case "application/vnd.oci.image.manifest.v1+json":
		return "OCI image manifest"
	case "application/vnd.docker.distribution.manifest.v2+json":
		return "Docker image manifest"
	}
	return cmp.Or(mediaType, "Unknown")
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

// createdFact is a platform card's Created row: the time, or "Unknown".
func createdFact(t time.Time) map[string]any {
	if t.IsZero() {
		return map[string]any{"Label": "Created", "Value": "Unknown"}
	}
	return map[string]any{"Label": "Created", "Time": t}
}
