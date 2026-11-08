package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

const (
	tagsPerPage = 100
	// resolveLimit caps the manifest lookups one page makes at once.
	resolveLimit = 8
	// sameAsShown is how many "Same as" tags a card names before "+N".
	sameAsShown      = 3
	labelDescription = "org.opencontainers.image.description"
	labelSource      = "org.opencontainers.image.source"
)

// sigTag matches cosign's signature, attestation and SBOM tags.
var sigTag = regexp.MustCompile(`^sha256-([0-9a-f]{64})\.(sig|att|sbom)$`)

// cosign gives a cosign tag suffix's badge and the command for it.
var cosign = map[string]struct{ badge, cmd string }{
	"sig":  {"signature", "cosign verify"},
	"att":  {"attestation", "cosign verify-attestation"},
	"sbom": {"sbom", "cosign download sbom"},
}

// repoView is the data for the "repo" and "repo-tags" templates.
type repoView struct {
	Repo, Namespace, Name string
	Prefix                string // "library/", shown dimmed before Name
	Helm                  bool
	Description           string
	Pills                 []pill
	TagCount              int    // every tag in the repo
	Query, View           string // the tag filter and view, kept in links
	Cards                 []tagCard
	Page, Pages           int
	Prev, Next            string // pagination links, "" at either end

	// The Overview tab: the tag whose labels fill About, its source link,
	// every tag for the dropdown, and the selected tag's summary.
	Overview         bool
	LabelTag, Source string
	Tags             []string
	Summary          tagCard
}

type pill struct {
	Icon, Text, Copy string // Copy: optional text for the pill's copy button
	Mono             bool
}

type badge struct{ Label, Variant string }

// tagCard is one tag on the Tags tab. Until its details load, only Repo,
// Tag, Label, Badges and Load are set.
type tagCard struct {
	Repo, Tag, Label string
	Load             string // URL of the card with its details, while they're still to come
	Badges           []badge
	Digest           string
	Created          time.Time
	SameAs           []string // other tags on the page with this digest, the first few
	MoreSameAs       int      // how many more there are
	Command          string   // pull or verify command; "" when there's none
	Type             string   // what a tag without platforms holds, e.g. "Helm chart"
	Size             int64
	Main             string // the platform Size and Created are from, when there are several
	Platforms        []registry.Platform
}

// Repo serves a repository's page: the Tags tab in the cards view, or the
// Overview tab. The Tags tab's fragments are "tags", the filtered list, and
// "card", one card's details.
func (s *Server) Repo(w http.ResponseWriter, r *http.Request, ref web.Ref) {
	tags, err := s.Src.Tags(r.Context(), ref.Repo)
	if err != nil {
		web.Error(w, r, err)
		return
	}
	format.SortTags(tags)

	q := r.URL.Query()
	v := repoView{Repo: ref.Repo, TagCount: len(tags), Query: strings.TrimSpace(q.Get("q")), View: q.Get("view")}
	v.Namespace, v.Name = splitRepo(ref.Repo)
	if v.Namespace != "" {
		v.Prefix = v.Namespace + "/"
	}
	if q.Get("tab") == "overview" {
		s.overview(w, r, &v, tags)
		return
	}
	matches := tags
	if v.Query != "" {
		f := strings.ToLower(v.Query)
		matches = slices.DeleteFunc(slices.Clone(tags), func(t string) bool { return !strings.Contains(strings.ToLower(t), f) })
	}
	v.Pages = max(1, (len(matches)+tagsPerPage-1)/tagsPerPage)
	v.Page, _ = strconv.Atoi(q.Get("page"))
	v.Page = min(max(v.Page, 1), v.Pages)
	page := matches[(v.Page-1)*tagsPerPage : min(v.Page*tagsPerPage, len(matches))]

	switch web.Fragment(r) {
	case "card":
		s.repoCard(w, r, ref.Repo, page, q.Get("tag"))
		return
	case "tags":
		// The filter is typed into the page, so keep its URL in step without the fragment.
		w.Header().Set("HX-Replace-Url", v.link(r))
		v.cards(r, page)
		s.fragment(w, r, "repo-tags", v)
		return
	}

	v.cards(r, page)
	s.header(r.Context(), &v, tags)
	s.page(w, r, ui.Page{PageTitle: ref.Repo}, "repo", v)
}

// header fills in the pills, labels and tile from the latest tag, or the
// newest if there's no latest. It's decoration, so a tag that can't be read
// just leaves them out; its card shows the error.
func (s *Server) header(ctx context.Context, v *repoView, tags []string) {
	pills := []pill{{Icon: "tag", Text: plural(len(tags), "tag")}}
	defer func() { v.Pills = pills }()
	// SortTags pins latest first. Skip cosign tags, which sort above versions.
	i := slices.IndexFunc(tags, func(t string) bool { return !sigTag.MatchString(t) })
	if i < 0 {
		return
	}
	v.LabelTag = tags[i]
	m, err := s.Src.Resolve(ctx, v.Repo, tags[i])
	if err != nil {
		return
	}
	v.Helm = m.Kind == registry.KindHelm
	p, ok := mainPlatform(m)
	if !ok {
		return
	}
	pills = append(pills, pill{Icon: "cpu", Text: plural(len(m.Platforms), "platform")})
	cfg, err := s.Src.Config(ctx, v.Repo, p.Config)
	if err != nil {
		return
	}
	v.Description = cfg.Labels[labelDescription]
	// Labels are whatever the image's author wrote; only web links become links.
	if src := cfg.Labels[labelSource]; strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") {
		v.Source = src
	}
	if !cfg.Created.IsZero() {
		pills = append(pills, pill{Icon: "clock", Text: "Created " + format.Age(cfg.Created)})
	}
}

// cards fills in the page's cards, each waiting to load its details.
func (v *repoView) cards(r *http.Request, page []string) {
	v.Cards = make([]tagCard, len(page))
	for i, t := range page {
		v.Cards[i] = tagCard{
			Repo: v.Repo, Tag: t, Label: tagLabel(t), Badges: latestBadge(t),
			Load: v.link(r, "fragment", "card", "tag", t),
		}
	}
	if v.Page > 1 {
		v.Prev = v.link(r, "page", strconv.Itoa(v.Page-1))
	}
	if v.Page < v.Pages {
		v.Next = v.link(r, "page", strconv.Itoa(v.Page+1))
	}
}

// link returns this page's URL with the filter, view and page kept and kv
// (key, value pairs) set on top.
func (v *repoView) link(r *http.Request, kv ...string) string {
	q := url.Values{}
	for k, val := range map[string]string{"q": v.Query, "view": v.View} {
		if val != "" {
			q.Set(k, val)
		}
	}
	if v.Page > 1 {
		q.Set("page", strconv.Itoa(v.Page))
	}
	for i := 0; i+1 < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	if len(q) == 0 {
		return r.URL.EscapedPath()
	}
	return r.URL.EscapedPath() + "?" + q.Encode()
}

// repoCard answers one card's details. "Same as" needs the digest of every
// tag on the page, so they're all resolved; the cache shares that work
// between the cards loading at once.
func (s *Server) repoCard(w http.ResponseWriter, r *http.Request, repo string, page []string, tag string) {
	i := slices.Index(page, tag)
	if i < 0 {
		web.Error(w, r, fmt.Errorf("tag %s:%s on this page: %w", repo, tag, registry.ErrNotFound))
		return
	}
	ms, errs := s.resolveAll(r.Context(), repo, page)
	if errs[i] != nil {
		web.Error(w, r, errs[i])
		return
	}
	c, err := s.card(r.Context(), repo, tag, ms[i])
	if err != nil {
		web.Error(w, r, err)
		return
	}
	for j, o := range ms {
		if j != i && errs[j] == nil && o.Digest == c.Digest {
			c.SameAs = append(c.SameAs, page[j])
		}
	}
	if n := len(c.SameAs); n > sameAsShown {
		c.SameAs, c.MoreSameAs = c.SameAs[:sameAsShown], n-sameAsShown
	}
	s.fragment(w, r, "repo-card", c)
}

// card returns a tag's details from m, its manifest; all but SameAs.
func (s *Server) card(ctx context.Context, repo, tag string, m registry.Manifest) (tagCard, error) {
	c := tagCard{Repo: repo, Tag: tag, Label: tagLabel(tag), Badges: latestBadge(tag), Digest: m.Digest, Size: m.Size, Platforms: m.Platforms}
	ref := s.PullHost + "/" + repo
	switch m.Kind {
	case registry.KindImage, registry.KindIndex:
		c.Command = "docker pull " + ref + ":" + tag
	case registry.KindHelm:
		c.Badges = append(c.Badges, badge{"helm chart", "helm"})
		c.Command = "helm pull oci://" + ref + " --version " + tag
		c.Type = "Helm chart"
	case registry.KindSignature:
		b, c2 := "signature", ""
		// A cosign tag names the digest it signs: sha256-<hex>.sig.
		if sm := sigTag.FindStringSubmatch(tag); sm != nil {
			b, c2 = cosign[sm[2]].badge, cosign[sm[2]].cmd+" "+ref+"@sha256:"+sm[1]
		}
		c.Badges = append(c.Badges, badge{b, "signature"})
		c.Command, c.Type = c2, "Cosign "+b
	default:
		c.Badges = append(c.Badges, badge{"artifact", ""})
		c.Type = "OCI artifact"
	}

	if p, ok := mainPlatform(m); ok {
		cfg, err := s.Src.Config(ctx, repo, p.Config)
		if err != nil {
			return c, err
		}
		c.Created, c.Size = cfg.Created, p.Size
		if len(m.Platforms) > 1 {
			c.Main = format.Platform(p)
		}
	}
	return c, nil
}

// overview serves the Overview tab. Its fragment "summary" is the summary
// of ?tag=, which the tag dropdown swaps in.
func (s *Server) overview(w http.ResponseWriter, r *http.Request, v *repoView, tags []string) {
	v.Overview, v.Tags = true, tags
	tag, summary := r.URL.Query().Get("tag"), web.Fragment(r) == "summary"
	if !summary {
		s.header(r.Context(), v, tags)
	}
	if !slices.Contains(tags, tag) {
		if summary {
			web.Error(w, r, fmt.Errorf("tag %s:%s: %w", v.Repo, tag, registry.ErrNotFound))
			return
		}
		tag = v.LabelTag
	}
	if tag != "" {
		m, err := s.Src.Resolve(r.Context(), v.Repo, tag)
		if err == nil {
			v.Summary, err = s.card(r.Context(), v.Repo, tag, m)
		}
		if err != nil {
			web.Error(w, r, err)
			return
		}
	}
	if summary {
		// The dropdown changes the page, so keep its URL in step without the fragment.
		w.Header().Set("HX-Replace-Url", r.URL.EscapedPath()+"?"+url.Values{"tab": {"overview"}, "tag": {tag}}.Encode())
		s.fragment(w, r, "repo-summary", v.Summary)
		return
	}
	s.page(w, r, ui.Page{PageTitle: v.Repo}, "repo", v)
}

// resolveAll resolves tags in parallel, resolveLimit at a time.
func (s *Server) resolveAll(ctx context.Context, repo string, tags []string) ([]registry.Manifest, []error) {
	ms := make([]registry.Manifest, len(tags))
	errs := make([]error, len(tags))
	var g errgroup.Group
	g.SetLimit(resolveLimit)
	for i, t := range tags {
		g.Go(func() error {
			ms[i], errs[i] = s.Src.Resolve(ctx, repo, t)
			return nil
		})
	}
	_ = g.Wait() // errors are per tag, in errs
	return ms, errs
}

// mainPlatform is the platform that speaks for a tag: linux/amd64, or the
// first. It reports false for tags without platforms, such as Helm charts.
func mainPlatform(m registry.Manifest) (registry.Platform, bool) {
	for _, p := range m.Platforms {
		if p.OS == "linux" && p.Arch == "amd64" {
			return p, true
		}
	}
	if len(m.Platforms) == 0 {
		return registry.Platform{}, false
	}
	return m.Platforms[0], true
}

// tagLabel shortens cosign tags, "sha256-<64 hex>.sig", to their first 12
// hex characters; other tags are shown as they are.
func tagLabel(tag string) string {
	if m := sigTag.FindStringSubmatch(tag); m != nil {
		return "sha256-" + m[1][:12] + "…." + m[2]
	}
	return tag
}

func latestBadge(tag string) []badge {
	if tag == "latest" {
		return []badge{{"latest", "accent"}}
	}
	return nil
}

// splitRepo splits "library/nginx" into "library" and "nginx".
func splitRepo(repo string) (namespace, name string) {
	i := strings.LastIndexByte(repo, '/')
	if i < 0 {
		return "", repo
	}
	return repo[:i], repo[i+1:]
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
