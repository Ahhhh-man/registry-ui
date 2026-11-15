package pages

import (
	"cmp"
	"context"
	"errors"
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
	// resolveLimit caps the manifest lookups one page makes at once.
	resolveLimit = 8
	// sameAsShown is how many "Same as" tags a card names before "+N".
	sameAsShown = 3
	// imagesShown caps the tags "Tags by image" looks up, so one panel
	// doesn't ask the registry about thousands of tags.
	imagesShown      = 500
	labelDescription = "org.opencontainers.image.description"
	labelSource      = "org.opencontainers.image.source"
	labelURL         = "org.opencontainers.image.url"
	labelDocs        = "org.opencontainers.image.documentation"
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
	kind                  string // the header tag's ?type=, "" if unknown
	Description           string
	Pills                 []pill
	TagCount              int    // every tag in the repo
	Query, View, Sort     string // the tag filter, view and ?sort=, kept in links; Sort is "" for the default order
	order                 string // the order the tags are in: Sort, or the default's name
	Type                  string // the ?type= filter, also kept
	Menu                  sortMenu
	Chips                 []typeChip
	Note                  string // why the tags aren't filtered or in the Sort order, when they aren't
	Empty                 string // the title when a filter leaves no tags
	Matches               int    // tags the filter keeps, on every page
	List                  bool   // the list view: ?view=list, or the view cookie without ?view=
	TagTimes              bool   // the source knows when tags were updated: storage mode
	Storage               bool   // storage mode, which only reads, for an empty repo's explanation
	Cards                 []tagCard
	Page, Pages           int
	Prev, Next            string // pagination links, "" at either end

	// The Overview tab: the tag whose labels fill About, its links,
	// "Tags by image" while it loads, every tag for the dropdown, and the
	// selected tag's summary.
	Overview bool
	LabelTag string
	Links    []link
	Images   imagesView
	Tags     []string
	Summary  tagCard
}

// imagesView is the data for "repo-images", the Overview's "Tags by image":
// a line of tags per image, in the order of each line's first tag.
type imagesView struct {
	Repo         string
	Load         string // URL of the panel with its lines, while they're still to come
	Images       [][]tagChip
	Shown, Total int    // Shown is less than Total when a big repo is cut short
	Missing      int    // tags left out because their manifest is missing
	Note         string // why some tags are left out, when the registry was too slow
}

type tagChip struct{ Tag, Label string }

type pill struct {
	Icon, Text, Copy string    // Copy: optional text for the pill's copy button
	Title            string    // optional hover text
	Time             time.Time // optional: Text goes before its age, and Title's {} is where the time goes
	Mono             bool
	Links            []link // optional, after Text
}

type link struct{ Label, Href string }

type badge struct{ Label, Variant string }

// tagCard is one tag on the Tags tab, a card or a list row. Until its
// details load, only Repo, Tag, Label, Badges, Load and, in the list view,
// a best guess at Command are set.
type tagCard struct {
	Repo, Tag, Label string
	Load             string // URL of the card with its details, while they're still to come
	Badges           []badge
	Digest           string
	Created          time.Time
	SameAs           []string // other tags on the page with this digest, the first few
	MoreSameAs       int      // how many more there are
	Command          string   // pull or verify command; "" when there's none
	Ref              string   // "registry.example.com/library/nginx:1.27.3"; "" when the manifest is missing
	Type             string   // what a tag without platforms holds, e.g. "Helm chart"
	Size             int64
	Unknown          bool                // every platform is missing, so Size and Created aren't known
	Main             string              // the platform Size and Created are from, when there are several
	Platforms        []registry.Platform // missing ones last
	Updated          time.Time           // when the tag last changed on the registry; zero if unknown
	TagTimes         bool                // the source has tag times, so a list row has an Updated cell
	Missing          bool                // the tag's manifest is missing: only Repo, Tag, Label, Badges and the times are set
}

// Found is what a screen reader hears once the tag filter has swapped the
// list: how many tags matched across every page, or Empty.
func (v repoView) Found() string {
	if v.Matches == 0 {
		return v.Empty
	}
	return plural(v.Matches, "tag")
}

// PlatformCount is how many platforms the tag has, e.g. "3 platforms (1 missing)".
func (c tagCard) PlatformCount() string { return platformCount(c.Platforms) }

// MorePlatforms labels the platforms a card hides behind its first three,
// e.g. "1 more platform (2 missing)".
func (c tagCard) MorePlatforms() string {
	return strings.Replace(platformCount(c.Platforms[min(3, len(c.Platforms)):]), " platform", " more platform", 1)
}

// Repo serves a repository's page: the Tags tab in the cards or list view,
// or the Overview tab. The Tags tab's fragments are "tags", the filtered
// list, and "card" or "row", one tag's details.
func (s *Server) Repo(w http.ResponseWriter, r *http.Request, ref web.Ref) {
	tags, err := s.Src.Tags(r.Context(), ref.Repo)
	if err != nil {
		web.Error(w, r, err)
		return
	}
	format.SortTags(tags)

	q := r.URL.Query()
	v := repoView{
		Repo: ref.Repo, TagCount: len(tags), Query: strings.TrimSpace(q.Get("q")), View: q.Get("view"),
		Type: pickSort(contentTypes, q.Get("type")), TagTimes: s.tagTimes(r.Context()), Storage: s.Storage,
	}
	def := cmp.Or(s.TagSort, "version")
	choices := withDefault(sorts(tagSorts, v.TagTimes), def)
	v.Sort = pickSort(choices, q.Get("sort"))
	v.order = cmp.Or(v.Sort, def)
	v.Namespace, v.Name = splitRepo(ref.Repo)
	if v.Namespace != "" {
		v.Prefix = v.Namespace + "/"
	}
	v.List = v.View == "list"
	if c, err := r.Cookie("view"); err == nil && v.View == "" {
		v.List = c.Value == "list"
	}
	if !tagsTab(q, web.Fragment(r)) {
		s.overview(w, r, &v, tags)
		return
	}
	f := strings.ToLower(v.Query)
	matches := slices.DeleteFunc(slices.Clone(tags), func(t string) bool { return !strings.Contains(strings.ToLower(t), f) })
	matches, counts, note := s.filterTags(r.Context(), ref.Repo, matches, v.Type)
	v.Matches = len(matches)
	v.Note = cmp.Or(s.sortTags(r.Context(), ref.Repo, matches, v.order), note)
	per := cmp.Or(s.TagsPerPage, 100)
	v.Pages = max(1, (len(matches)+per-1)/per)
	v.Page, _ = strconv.Atoi(q.Get("page"))
	v.Page = min(max(v.Page, 1), v.Pages)
	page := matches[(v.Page-1)*per : min(v.Page*per, len(matches))]

	switch web.Fragment(r) {
	case "card":
		s.repoCard(w, r, ref.Repo, page, q.Get("tag"), "repo-card")
		return
	case "row":
		s.repoCard(w, r, ref.Repo, page, q.Get("tag"), "tag-row")
		return
	}

	// The page and the tags fragment show the chips, and the header first
	// tells cards a Helm repo and the chips what the repo holds.
	s.header(r.Context(), &v, tags)
	if v.Type == "" && v.kind != "" && counts[v.kind] == 0 {
		// shortcut: unfiltered, the repo's untyped tags are taken to be the
		// header tag's kind, so a mixed repo shows its other chips only once a
		// type is picked; resolve every tag in filterTags if that matters.
		counts[v.kind] = -1
	}
	v.Chips = typeChips(v.Type, counts, func(t string) string { return v.link(r, "type", t, "page", "") })
	v.Empty = noMatch("tags", v.Type, v.Query)

	v.cards(r, page, s.PullHost)
	s.loadCards(r.Context(), &v, page)
	if web.Fragment(r) == "tags" {
		// The filter is typed into the page, so keep its URL in step without the fragment.
		w.Header().Set("HX-Replace-Url", v.link(r))
		s.fragment(w, r, "repo-tags", v)
		return
	}
	v.Menu = newSortMenu(choices, v.Sort, func(sort string) string { return v.link(r, "sort", sort, "page", "") })
	s.page(w, r, ui.Page{PageTitle: ref.Repo}, "repo", v)
}

// filterTags keeps the tags of type t and counts each type for the chips.
// Signatures are known by name; with a type picked, every tag is resolved
// for the rest, and without one they're left uncounted. If resolving takes
// too long the tags stay unfiltered and the returned note says so.
func (s *Server) filterTags(ctx context.Context, repo string, tags []string, t string) ([]string, map[string]int, string) {
	counts := map[string]int{"": len(tags)}
	if t == "" {
		for _, tag := range tags {
			if sigTag.MatchString(tag) {
				counts["signature"]++
			}
		}
		return tags, counts, ""
	}
	types := make([]string, len(tags))
	err := fetchAll(ctx, len(tags), func(ctx context.Context, i int) {
		if sigTag.MatchString(tags[i]) {
			types[i] = "signature"
		} else if m, err := s.Src.Resolve(ctx, repo, tags[i]); err == nil {
			types[i] = typeOf(m.Kind)
		}
	})
	if err != nil {
		for _, c := range contentTypes[1:] {
			counts[c.Value] = -1
		}
		return tags, counts, "The registry took too long to describe every tag, so this list isn't filtered by type."
	}
	var kept []string
	for i, tt := range types {
		if tt != "" {
			counts[tt]++
		}
		if tt == t {
			kept = append(kept, tags[i])
		}
	}
	return kept, counts, ""
}

// labelTag is the index of the tag that describes the repo: SortTags pins
// latest first, so the first that isn't a cosign tag, which sort above
// versions. It's -1 if there's none.
func labelTag(tags []string) int {
	return slices.IndexFunc(tags, func(t string) bool { return !sigTag.MatchString(t) })
}

// sortTags puts tags, which arrive newest version first, in the order by.
// The created and size orders read a summary of every tag, and the updated
// order reads every tag time; if that takes too long the tags stay in
// version order and the returned note says so.
func (s *Server) sortTags(ctx context.Context, repo string, tags []string, by string) string {
	switch by {
	case "version-asc":
		slices.Reverse(tags)
		if n := len(tags); n > 0 && tags[n-1] == "latest" { // still pinned first
			copy(tags[1:], tags[:n-1])
			tags[0] = "latest"
		}
		return ""
	case "name":
		slices.Sort(tags)
		return ""
	case "created", "size", "updated":
	default:
		return ""
	}
	type tagInfo struct {
		tag     string
		updated time.Time
		tagSummary
	}
	infos := make([]tagInfo, len(tags))
	err := fetchAll(ctx, len(tags), func(ctx context.Context, i int) {
		infos[i].tag = tags[i]
		if by == "updated" {
			infos[i].updated, _ = s.Src.TagTime(ctx, repo, tags[i]) // zero on error: sorts last
			return
		}
		// What's missing on an error sorts last, and the card shows the error.
		infos[i].tagSummary, _ = registry.Summary(ctx, s.Src, func(ctx context.Context, src registry.Source) (tagSummary, error) {
			return readTagSummary(ctx, src, repo, tags[i])
		}, "tag sort", repo, tags[i])
	})
	if err != nil {
		return "The registry took too long to describe every tag, so this list is in version order instead."
	}
	slices.SortStableFunc(infos, func(a, b tagInfo) int {
		switch by {
		case "size":
			return cmp.Compare(b.Size, a.Size)
		case "updated":
			return newestFirst(a.updated, b.updated)
		}
		return newestFirst(a.Created, b.Created)
	})
	for i, in := range infos {
		tags[i] = in.tag
	}
	return ""
}

// tagSummary is what the created and size sorts compare of a tag. It's
// kept as one small summary per tag rather than the manifest and config
// it's read from, so a sort of thousands of tags fits in the cache.
type tagSummary struct {
	Created time.Time
	Size    int64
}

// readTagSummary reads tag's summary from src. Its size is the main
// platform's, or the manifest's if there's none.
func readTagSummary(ctx context.Context, src registry.Source, repo, tag string) (tagSummary, error) {
	m, err := src.Resolve(ctx, repo, tag)
	if err != nil {
		return tagSummary{}, err
	}
	p, ok := mainPlatform(m)
	if !ok {
		return tagSummary{Size: m.Size}, nil
	}
	sum := tagSummary{Size: p.Size}
	cfg, err := src.Config(ctx, repo, p.Config)
	if err != nil {
		return sum, err
	}
	sum.Created = cmp.Or(cfg.Created, p.Created)
	return sum, nil
}

// loadCards fills in the page's cards up front after a sort that has
// already fetched their details, so they don't each ask again. A card that
// fails keeps loading on its own, to show its error.
func (s *Server) loadCards(ctx context.Context, v *repoView, page []string) {
	if (v.order != "created" && v.order != "size") || v.Note != "" {
		return
	}
	ms, errs := s.resolveAll(ctx, v.Repo, page)
	_ = fetchAll(ctx, len(page), func(ctx context.Context, i int) {
		if c, err := s.tagDetails(ctx, v.Repo, page, ms, errs, i); err == nil {
			v.Cards[i] = c
		}
	})
}

// header fills in the pills, labels and tile from the latest tag, or the
// newest if there's no latest. It's decoration, so a tag that can't be read
// just leaves them out; its card shows the error.
func (s *Server) header(ctx context.Context, v *repoView, tags []string) {
	pills := []pill{{Icon: "tag", Text: plural(len(tags), "tag")}}
	if n, err := s.Src.DiskUsage(ctx, v.Repo); err == nil {
		pills = append(pills, pill{Icon: "hard-drive", Text: format.Size(n) + " on disk", Title: "Layers shared with other repositories count in each of them, so repository totals add up to more than the registry total."})
	}
	defer func() { v.Pills = pills }()
	i := labelTag(tags)
	if i < 0 {
		return
	}
	v.LabelTag = tags[i]
	m, err := s.Src.Resolve(ctx, v.Repo, tags[i])
	if err != nil {
		return
	}
	v.Helm, v.kind = m.Kind == registry.KindHelm, typeOf(m.Kind)
	if len(m.Platforms) > 0 {
		pills = append(pills, pill{Icon: "cpu", Text: platformCount(m.Platforms)})
	}
	p, ok := mainPlatform(m)
	if !ok {
		return
	}
	cfg, err := s.Src.Config(ctx, v.Repo, p.Config)
	if err != nil {
		return
	}
	v.Description = cfg.Labels[labelDescription]
	// Labels are whatever the image's author wrote; only web links become links.
	for _, l := range []link{{"Source", labelSource}, {"Homepage", labelURL}, {"Documentation", labelDocs}} {
		u := cfg.Labels[l.Href]
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			continue
		}
		// docker/metadata-action sets url to the source: show that once.
		if l.Href == labelURL && strings.TrimSuffix(u, "/") == strings.TrimSuffix(cfg.Labels[labelSource], "/") {
			continue
		}
		v.Links = append(v.Links, link{l.Label, u})
	}
	if created := cmp.Or(cfg.Created, p.Created); !created.IsZero() {
		pills = append(pills, pill{Icon: "clock", Text: "Created ", Time: created})
	}
}

// cards fills in the page's cards or rows, each waiting to load its
// details. A row also gets a guess at its command, so a selection can be
// copied before every row has loaded.
func (v *repoView) cards(r *http.Request, page []string, host string) {
	frag, kind := "card", registry.KindImage
	if v.Helm {
		kind = registry.KindHelm
	}
	if v.List {
		frag = "row"
	}
	v.Cards = make([]tagCard, len(page))
	for i, t := range page {
		v.Cards[i] = tagCard{
			Repo: v.Repo, Tag: t, Label: tagLabel(t), Badges: latestBadge(t), TagTimes: v.TagTimes,
			Load: v.link(r, "fragment", frag, "tag", t, "tab", ""), // a fragment is always Tags
		}
		if v.List {
			k := kind
			if sigTag.MatchString(t) {
				k = registry.KindSignature
			}
			v.Cards[i].Command, v.Cards[i].Ref = command(host, v.Repo, t, k), host+"/"+v.Repo+":"+t
		}
	}
	if v.Page > 1 {
		v.Prev = v.link(r, "page", strconv.Itoa(v.Page-1))
	}
	if v.Page < v.Pages {
		v.Next = v.link(r, "page", strconv.Itoa(v.Page+1))
	}
}

// link returns this page's URL with the filters, view, sort and page kept
// and kv (key, value pairs) set on top; an empty value drops its key.
func (v *repoView) link(r *http.Request, kv ...string) string {
	page := ""
	if v.Page > 1 {
		page = strconv.Itoa(v.Page)
	}
	return pageURL(r.URL.EscapedPath(), append([]string{"tab", "tags", "q", v.Query, "type", v.Type, "view", v.View, "sort", v.Sort, "page", page}, kv...)...)
}

// tagsTab reports whether a request is for the Tags tab rather than
// Overview, the default: ?tab=tags, a Tags fragment, or a link from before
// Overview came first that carries a Tags-only parameter.
func tagsTab(q url.Values, fragment string) bool {
	switch q.Get("tab") {
	case "tags":
		return true
	case "overview":
		return false
	}
	if fragment != "" {
		return fragment != "summary" && fragment != "images"
	}
	return q.Has("q") || q.Has("view") || q.Has("sort") || q.Has("type") || q.Has("page")
}

// repoCard answers one card's or row's details, rendered with the template
// name. "Same as" needs the digest of every tag on the page, so they're all
// resolved; the cache shares that work between the cards loading at once.
func (s *Server) repoCard(w http.ResponseWriter, r *http.Request, repo string, page []string, tag, name string) {
	i := slices.Index(page, tag)
	if i < 0 {
		web.Error(w, r, fmt.Errorf("tag %s:%s on this page: %w", repo, tag, registry.ErrNotFound))
		return
	}
	ms, errs := s.resolveAll(r.Context(), repo, page)
	c, err := s.tagDetails(r.Context(), repo, page, ms, errs, i)
	if err != nil {
		web.Error(w, r, err)
		return
	}
	s.fragment(w, r, name, c)
}

// tagDetails returns the card for page[i], given every tag on the page
// resolved (ms and errs, from resolveAll) for its "Same as".
func (s *Server) tagDetails(ctx context.Context, repo string, page []string, ms []registry.Manifest, errs []error, i int) (tagCard, error) {
	missing := manifestMissing(errs[i], page, page[i])
	if errs[i] != nil && !missing {
		return tagCard{}, errs[i]
	}
	c, err := s.card(ctx, repo, page[i], ms[i], missing)
	if err != nil || missing {
		return c, err // a missing manifest has no digest to share
	}
	for j, o := range ms {
		if j != i && errs[j] == nil && o.Digest == c.Digest {
			c.SameAs = append(c.SameAs, page[j])
		}
	}
	if n := len(c.SameAs); n > sameAsShown {
		c.SameAs, c.MoreSameAs = c.SameAs[:sameAsShown], n-sameAsShown
	}
	return c, nil
}

// card returns a tag's details from m, its manifest; all but SameAs. A tag
// whose manifest is missing gets only a badge saying so and when it was
// updated: no pull command, since the pull would fail.
func (s *Server) card(ctx context.Context, repo, tag string, m registry.Manifest, missing bool) (tagCard, error) {
	c := tagCard{Repo: repo, Tag: tag, Label: tagLabel(tag), Badges: latestBadge(tag)}
	var err error
	c.Updated, err = s.Src.TagTime(ctx, repo, tag) // decoration: an error just leaves it out
	c.TagTimes = !errors.Is(err, errors.ErrUnsupported)
	if missing {
		c.Missing, c.Badges = true, append(c.Badges, badge{"manifest missing", "warn"})
		return c, nil
	}
	c.Digest, c.Size, c.Platforms = m.Digest, m.Size, availableFirst(m.Platforms)
	c.Command, c.Ref = command(s.PullHost, repo, tag, m.Kind), s.PullHost+"/"+repo+":"+tag
	switch m.Kind {
	case registry.KindImage, registry.KindIndex: // the platforms say it all
	case registry.KindHelm:
		c.Badges = append(c.Badges, badge{"helm chart", "helm"})
		c.Type = "Helm chart"
	case registry.KindSignature:
		b := "signature"
		if sm := sigTag.FindStringSubmatch(tag); sm != nil {
			b = cosign[sm[2]].badge
		}
		c.Badges = append(c.Badges, badge{b, "signature"})
		c.Type = "Cosign " + b
	default:
		c.Badges = append(c.Badges, badge{"artifact", ""})
		c.Type = "OCI artifact"
	}

	if p, ok := mainPlatform(m); ok {
		cfg, err := s.Src.Config(ctx, repo, p.Config)
		if err != nil {
			return c, err
		}
		c.Created, c.Size = cmp.Or(cfg.Created, p.Created), p.Size
		if len(m.Platforms) > 1 {
			c.Main = format.Platform(p)
		}
	} else if len(m.Platforms) > 0 {
		c.Unknown = true
	}
	return c, nil
}

// command is the pull, or cosign verify, command for a tag of the given
// kind; "" for kinds that have none.
func command(host, repo, tag string, kind registry.Kind) string {
	ref := host + "/" + repo
	switch kind {
	case registry.KindImage, registry.KindIndex:
		return "docker pull " + ref + ":" + tag
	case registry.KindHelm:
		return "helm pull oci://" + ref + " --version " + tag
	case registry.KindSignature:
		// A cosign tag names the digest it signs: sha256-<hex>.sig.
		if sm := sigTag.FindStringSubmatch(tag); sm != nil {
			return cosign[sm[2]].cmd + " " + ref + "@sha256:" + sm[1]
		}
	}
	return ""
}

// overview serves the Overview tab. Its fragment "summary" is the summary
// of ?tag=, which the tag dropdown swaps in, and "images" is "Tags by
// image", which loads on its own so its lookups don't hold up the page.
func (s *Server) overview(w http.ResponseWriter, r *http.Request, v *repoView, tags []string) {
	if web.Fragment(r) == "images" {
		s.images(w, r, v.Repo, tags)
		return
	}
	v.Overview, v.Tags = true, tags
	v.Images = imagesView{Repo: v.Repo, Load: r.URL.EscapedPath() + "?fragment=images"}
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
		if missing := manifestMissing(err, tags, tag); err == nil || missing {
			v.Summary, err = s.card(r.Context(), v.Repo, tag, m, missing)
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

// images answers "Tags by image": tags, which arrive in version order, as a
// line per digest. A cosign tag names the digest it signs, so it joins
// that image's line, after its tags, without a lookup. Only the first
// imagesShown tags are looked up.
func (s *Server) images(w http.ResponseWriter, r *http.Request, repo string, tags []string) {
	v := imagesView{Repo: repo, Total: len(tags)}
	tags = tags[:min(len(tags), imagesShown)]
	v.Shown = len(tags)
	digests := make([]string, len(tags))
	errs := make([]error, len(tags))
	if fetchAll(r.Context(), len(tags), func(ctx context.Context, i int) {
		if sm := sigTag.FindStringSubmatch(tags[i]); sm != nil {
			digests[i] = "sha256:" + sm[1]
		} else {
			digests[i], errs[i] = s.Src.Digest(ctx, repo, tags[i])
		}
	}) != nil {
		v.Note = "The registry took too long to answer for every tag, so some are left out."
	}
	for _, err := range errs {
		switch {
		case err == nil, v.Note != "":
		case errors.Is(err, registry.ErrNotFound):
			v.Missing++
		default:
			web.Error(w, r, err)
			return
		}
	}
	line := map[string]int{}
	for _, sigs := range []bool{false, true} {
		for i, t := range tags {
			if digests[i] == "" || sigTag.MatchString(t) != sigs {
				continue
			}
			j, ok := line[digests[i]]
			if !ok {
				j, line[digests[i]] = len(v.Images), len(v.Images)
				v.Images = append(v.Images, nil)
			}
			v.Images[j] = append(v.Images[j], tagChip{t, tagLabel(t)})
		}
	}
	s.fragment(w, r, "repo-images", v)
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

// manifestMissing reports whether tag, listed in tags, failed to resolve
// with err because its manifest is missing: the registry lists the tag but
// not the manifest it points at. Any other error, such as a 401 or a 5xx,
// is a failure, not a missing manifest.
func manifestMissing(err error, tags []string, tag string) bool {
	return errors.Is(err, registry.ErrNotFound) && slices.Contains(tags, tag)
}

// mainPlatform is the platform that speaks for a tag: linux/amd64, or the
// first. A missing platform has no size or config, so it never speaks. It
// reports false for tags without platforms, such as Helm charts, and for
// indexes whose platforms are all missing.
func mainPlatform(m registry.Manifest) (registry.Platform, bool) {
	i := slices.IndexFunc(m.Platforms, func(p registry.Platform) bool { return !p.Missing && p.OS == "linux" && p.Arch == "amd64" })
	if i < 0 {
		i = slices.IndexFunc(m.Platforms, func(p registry.Platform) bool { return !p.Missing })
	}
	if i < 0 {
		return registry.Platform{}, false
	}
	return m.Platforms[i], true
}

// platformCount counts the platforms in the registry and adds how many
// more the index lists, so four platforms with one missing don't quietly
// read as three: "3 platforms (1 missing)", or "4 platforms, all missing".
func platformCount(ps []registry.Platform) string {
	missing := 0
	for _, p := range ps {
		if p.Missing {
			missing++
		}
	}
	switch n := len(ps) - missing; {
	case missing == 0:
		return plural(n, "platform")
	case n == 0:
		return plural(missing, "platform") + ", all missing"
	default:
		return plural(n, "platform") + " (" + strconv.Itoa(missing) + " missing)"
	}
}

// availableFirst is ps with the missing platforms moved to the end.
func availableFirst(ps []registry.Platform) []registry.Platform {
	ps = slices.Clone(ps)
	slices.SortStableFunc(ps, func(a, b registry.Platform) int {
		switch {
		case a.Missing == b.Missing:
			return 0
		case a.Missing:
			return 1
		}
		return -1
	})
	return ps
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
