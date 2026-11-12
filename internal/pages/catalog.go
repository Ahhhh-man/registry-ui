package pages

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// catalogPage is the data for the "catalog" template.
type catalogPage struct {
	Host  string       // registry host, beside the title
	Total int          // repositories in the registry
	Disk  bool         // the source can tell disk usage, so the figures load it: storage mode
	Query string       // the ?q= filter
	Sort  string       // the ?sort= order, "" for the default
	Type  string       // the ?type= filter, "" for all
	View  string       // one of catalogViews
	Keep  string       // View when the URL names it, kept in links; "" when a cookie or the default picked it
	Menu  sortMenu     // the sort menu
	List  string       // the list's URL without the search, which htmx adds as you type
	Note  string       // why the rows aren't filtered or in the Sort order, when they aren't
	Empty string       // the title when no rows match
	Rows  []catalogRow // repositories matching Query and Type, in the Sort order
}

// catalogRow is one repository in the catalog: just its name until its
// details are loaded by the row fragment.
type catalogRow struct {
	Repo   string
	View   string // the view it renders in, one of catalogViews
	Loaded bool   // the details below are filled in
	Failed bool   // the details couldn't be read
	// Tags counts the repository's tags. The rest describe its latest (or
	// newest) tag.
	Tags        int
	Tag         string // its name, "" without one
	Description string
	Platforms   string // the ones in the registry, e.g. "linux/amd64, linux/arm64 (1 missing)"
	Command     string // its pull command, "" when there's none
	Helm        bool
	Type        string // its ?type=, "" for a repository without tags
	Created     time.Time
	updated     time.Time // its newest tag time, for the "updated" sort only
}

// Namespace is the part of Repo before its name, "library/" or "".
func (c catalogRow) Namespace() string { return c.Repo[:strings.LastIndexByte(c.Repo, '/')+1] }

// Name is Repo without its namespace.
func (c catalogRow) Name() string { return c.Repo[strings.LastIndexByte(c.Repo, '/')+1:] }

// Kind names what a repository that isn't an image holds: "Helm chart".
func (c catalogRow) Kind() string {
	switch c.Type {
	case "helm":
		return "Helm chart"
	case "signature":
		return "Signature"
	}
	return "Other artifact"
}

// catalogViews are the ?view= values.
var catalogViews = []string{"compact", "comfortable", "detailed"}

// catalogView is the view to render in: ?view=, else the catalog-view
// cookie app.js sets (the repository page owns "view"), else comfortable.
func catalogView(r *http.Request) string {
	v := r.URL.Query().Get("view")
	if c, err := r.Cookie("catalog-view"); err == nil && v == "" {
		v = c.Value
	}
	if !slices.Contains(catalogViews, v) {
		return "comfortable"
	}
	return v
}

// Catalog serves the repository list. Rows start as bare names and fetch
// their details (?fragment=row&repo=…) once scrolled into view, so the page
// itself costs a single catalog call however large the registry is. The
// created and tag-count sorts are the exception: they fetch every matching
// row's details first, so those rows render loaded, and so does a type
// filter, which needs every row's latest tag.
func (s *Server) Catalog(w http.ResponseWriter, r *http.Request, _ web.Ref) {
	switch web.Fragment(r) {
	case "row":
		s.catalogRow(w, r)
		return
	case "disk":
		s.catalogDisk(w, r)
		return
	}
	repos, err := s.Src.Catalog(r.Context())
	if err != nil {
		web.Error(w, r, err)
		return
	}
	q, path := strings.TrimSpace(r.URL.Query().Get("q")), r.URL.EscapedPath()
	def := cmp.Or(s.CatalogSort, "name")
	choices := withDefault(sorts(catalogSorts, s.tagTimes(r.Context())), def)
	data := catalogPage{Host: s.PullHost, Total: len(repos), Disk: s.diskUsage(r.Context()), Query: q, Sort: pickSort(choices, r.URL.Query().Get("sort")), Type: pickSort(contentTypes, r.URL.Query().Get("type")), View: catalogView(r)}
	if r.URL.Query().Get("view") == data.View {
		data.Keep = data.View
	}
	// A query ending in "/" is a namespace: it matches from the start of the name.
	match := strings.Contains
	if strings.HasSuffix(q, "/") {
		match = strings.HasPrefix
	}
	for _, name := range repos {
		if match(name, strings.ToLower(q)) {
			data.Rows = append(data.Rows, catalogRow{Repo: name, View: data.View})
		}
	}
	data.Rows, data.Note = s.filterRows(r.Context(), data.Rows, data.Type)
	data.Note = cmp.Or(s.sortRows(r.Context(), data.Rows, cmp.Or(data.Sort, def)), data.Note)
	data.Menu = newSortMenu(choices, data.Sort, func(sort string) string {
		return pageURL(path, "q", q, "sort", sort, "type", data.Type, "view", data.Keep)
	})
	data.List = pageURL(path, "sort", data.Sort, "type", data.Type, "view", data.Keep)
	data.Empty = noMatch("repositories", data.Type, q)
	s.page(w, r, ui.Page{PageTitle: "Repositories", Query: q, Sort: data.Sort, Type: data.Type, View: data.Keep}, "catalog", data)
}

// filterRows keeps the rows whose latest (or newest) tag is of type t,
// loading every row's details to find out. Without a type it costs nothing;
// if it takes too long, the returned note says why the rows aren't filtered.
func (s *Server) filterRows(ctx context.Context, rows []catalogRow, t string) ([]catalogRow, string) {
	if t == "" {
		return rows, ""
	}
	err := fetchAll(ctx, len(rows), func(ctx context.Context, i int) {
		if err := s.rowDetails(ctx, &rows[i]); err != nil {
			slog.WarnContext(ctx, "catalog row details", "repo", rows[i].Repo, "err", err)
			rows[i].Failed = true
		}
	})
	if err != nil {
		return rows, "The registry took too long to describe every repository, so this list isn't filtered by type."
	}
	return slices.DeleteFunc(rows, func(row catalogRow) bool { return row.Type != t }), ""
}

// sortRows puts rows, which arrive A–Z, in the order by. The created and
// tag-count orders load every row's details first, and the updated order
// every tag time; if that takes too long the rows stay A–Z and the returned
// note says so.
func (s *Server) sortRows(ctx context.Context, rows []catalogRow, by string) string {
	switch by {
	case "name-desc":
		slices.Reverse(rows)
		return ""
	case "created", "tags", "updated":
	default:
		return ""
	}
	err := fetchAll(ctx, len(rows), func(ctx context.Context, i int) {
		if by == "updated" {
			rows[i].updated = s.repoUpdated(ctx, rows[i].Repo)
			return
		}
		if rows[i].Loaded || rows[i].Failed { // the type filter got them
			return
		}
		if err := s.rowDetails(ctx, &rows[i]); err != nil {
			slog.WarnContext(ctx, "catalog row details", "repo", rows[i].Repo, "err", err)
			rows[i].Failed = true
		}
	})
	if err != nil {
		return "The registry took too long to describe every repository, so this list is A–Z instead."
	}
	slices.SortStableFunc(rows, func(a, b catalogRow) int {
		switch by {
		case "tags":
			return cmp.Compare(b.Tags, a.Tags)
		case "updated":
			return newestFirst(a.updated, b.updated)
		}
		return newestFirst(a.Created, b.Created)
	})
	return ""
}

// repoUpdated is the newest tag time in repo, or zero if none can be read.
func (s *Server) repoUpdated(ctx context.Context, repo string) time.Time {
	var newest time.Time
	tags, _ := s.Src.Tags(ctx, repo)
	for _, tag := range tags {
		if t, err := s.Src.TagTime(ctx, repo, tag); err == nil && t.After(newest) {
			newest = t
		}
	}
	return newest
}

// catalogRow answers a row fragment. A failure only marks the row, so the
// link to the repository stays usable.
func (s *Server) catalogRow(w http.ResponseWriter, r *http.Request) {
	row := catalogRow{Repo: r.URL.Query().Get("repo"), View: catalogView(r)}
	if err := s.rowDetails(r.Context(), &row); err != nil {
		slog.WarnContext(r.Context(), "catalog row details", "repo", row.Repo, "err", err)
		row.Failed = true
	}
	s.fragment(w, r, "repo-row", row)
}

// diskUsage reports whether the source can tell how much disk it uses, as
// storage mode can. "/" names no repository, so storage refuses it without
// walking anything.
func (s *Server) diskUsage(ctx context.Context) bool {
	_, err := s.Src.DiskUsage(ctx, "/")
	return !errors.Is(err, errors.ErrUnsupported)
}

// catalogDisk answers the On disk stat's fragment, which loads after the
// page because it walks the whole blob store. The figure is decoration, so
// a failure just leaves the stat out.
func (s *Server) catalogDisk(w http.ResponseWriter, r *http.Request) {
	n, err := s.Src.DiskUsage(r.Context(), "")
	if err != nil {
		slog.WarnContext(r.Context(), "registry disk usage", "err", err)
		return
	}
	s.fragment(w, r, "disk-stat", format.Size(n))
}

// rowDetails fills in row's tag count, then the name, description,
// platforms, pull command and created date of its latest (or newest) tag.
func (s *Server) rowDetails(ctx context.Context, row *catalogRow) error {
	tags, err := s.Src.Tags(ctx, row.Repo)
	if err != nil {
		return err
	}
	row.Tags, row.Loaded = len(tags), true
	tag := newestTag(tags)
	row.Tag = tag
	if tag == "" {
		if len(tags) > 0 { // only cosign tags: signatures kept apart from their images
			row.Type = "signature"
		}
		return nil
	}
	m, err := s.Src.Resolve(ctx, row.Repo, tag)
	if manifestMissing(err, tags, tag) {
		return nil // nothing to describe, but nothing failed either
	}
	if err != nil {
		return err
	}
	row.Helm, row.Type, row.Command = m.Kind == registry.KindHelm, typeOf(m.Kind), command(s.PullHost, row.Repo, tag, m.Kind)
	var names []string
	for _, p := range m.Platforms {
		if !p.Missing {
			names = append(names, format.Platform(p))
		}
	}
	row.Platforms = strings.Join(names, ", ")
	// A platform the registry lacks still counts, as on the tag's own page.
	switch missing := len(m.Platforms) - len(names); {
	case missing > 0 && len(names) == 0:
		row.Platforms = platformCount(m.Platforms)
	case missing > 0:
		row.Platforms += " (" + strconv.Itoa(missing) + " missing)"
	}
	p, ok := mainPlatform(m)
	if !ok {
		return nil
	}
	cfg, err := s.Src.Config(ctx, row.Repo, p.Config)
	if err != nil {
		return err
	}
	row.Created, row.Description = cfg.Created, cfg.Labels["org.opencontainers.image.description"]
	return nil
}

// newestTag picks the tag a repository is summarised by, the same one the
// repository page describes: see labelTag. It's "" if there's none.
func newestTag(tags []string) string {
	tags = slices.Clone(tags)
	format.SortTags(tags)
	if i := labelTag(tags); i >= 0 {
		return tags[i]
	}
	return ""
}
