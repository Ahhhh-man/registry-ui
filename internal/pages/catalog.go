package pages

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// catalogPage is the data for the "catalog" template.
type catalogPage struct {
	Host  string       // registry host, above the title
	Total int          // repositories in the registry
	Query string       // the ?q= filter
	Sort  string       // the ?sort= order, "" for A–Z
	Type  string       // the ?type= filter, "" for all
	Menu  sortMenu     // the sort menu
	Chips []typeChip   // the type filters
	List  string       // the list's URL without the search, which htmx adds as you type
	Note  string       // why the rows aren't filtered or in the Sort order, when they aren't
	Empty string       // the title when no rows match
	Rows  []catalogRow // repositories matching Query and Type, in the Sort order
}

// catalogRow is one repository in the catalog: just its name until its
// details are loaded by the row fragment.
type catalogRow struct {
	Repo   string
	Loaded bool // the details below are filled in
	Failed bool // the details couldn't be read
	// Tags counts the repository's tags. The rest describe its latest (or
	// newest) tag.
	Tags        int
	Description string
	Platforms   int
	Helm        bool
	Type        string // its ?type=, "" for a repository without tags
	Created     time.Time
}

// Namespace is the part of Repo before its name, "library/" or "".
func (c catalogRow) Namespace() string { return c.Repo[:strings.LastIndexByte(c.Repo, '/')+1] }

// Name is Repo without its namespace.
func (c catalogRow) Name() string { return c.Repo[strings.LastIndexByte(c.Repo, '/')+1:] }

// Age is how long ago Created was, or "" when unknown.
func (c catalogRow) Age() string {
	if c.Created.IsZero() {
		return ""
	}
	return format.Age(c.Created)
}

// Date is Created as a date.
func (c catalogRow) Date() string { return format.Date(c.Created) }

// Catalog serves the repository list. Rows start as bare names and fetch
// their details (?fragment=row&repo=…) once scrolled into view, so the page
// itself costs a single catalog call however large the registry is. The
// created and tag-count sorts are the exception: they fetch every matching
// row's details first, so those rows render loaded, and so does a type
// filter, which needs every row's latest tag.
func (s *Server) Catalog(w http.ResponseWriter, r *http.Request, _ web.Ref) {
	if web.Fragment(r) == "row" {
		s.catalogRow(w, r)
		return
	}
	repos, err := s.Src.Catalog(r.Context())
	if err != nil {
		web.Error(w, r, err)
		return
	}
	q, path := strings.TrimSpace(r.URL.Query().Get("q")), r.URL.EscapedPath()
	data := catalogPage{Host: s.PullHost, Total: len(repos), Query: q, Sort: pickSort(catalogSorts, r.URL.Query().Get("sort")), Type: pickSort(contentTypes, r.URL.Query().Get("type"))}
	for _, name := range repos {
		if strings.Contains(name, strings.ToLower(q)) {
			data.Rows = append(data.Rows, catalogRow{Repo: name})
		}
	}
	var counts map[string]int
	data.Rows, counts, data.Note = s.filterRows(r.Context(), data.Rows, data.Type)
	data.Note = cmp.Or(s.sortRows(r.Context(), data.Rows, data.Sort), data.Note)
	data.Menu = newSortMenu(catalogSorts, data.Sort, func(sort string) string {
		return pageURL(path, "q", q, "sort", sort, "type", data.Type)
	})
	data.Chips = typeChips(data.Type, counts, func(t string) string { return pageURL(path, "q", q, "sort", data.Sort, "type", t) })
	data.List = pageURL(path, "sort", data.Sort, "type", data.Type)
	data.Empty = noMatch("repositories", data.Type, q)
	s.page(w, r, ui.Page{PageTitle: "Repositories", Query: q, Sort: data.Sort, Type: data.Type}, "catalog", data)
}

// filterRows keeps the rows whose latest (or newest) tag is of type t,
// loading every row's details to find out, and counts each type for the
// chips. Without a type it costs nothing and leaves the counts unknown
// (-1); so does taking too long, when the returned note says why the rows
// aren't filtered.
func (s *Server) filterRows(ctx context.Context, rows []catalogRow, t string) ([]catalogRow, map[string]int, string) {
	counts := map[string]int{"": len(rows)}
	for _, c := range contentTypes[1:] {
		counts[c.Value] = -1
	}
	if t == "" {
		return rows, counts, ""
	}
	err := fetchAll(ctx, len(rows), func(ctx context.Context, i int) {
		if err := s.rowDetails(ctx, &rows[i]); err != nil {
			slog.WarnContext(ctx, "catalog row details", "repo", rows[i].Repo, "err", err)
			rows[i].Failed = true
		}
	})
	if err != nil {
		return rows, counts, "The registry took too long to describe every repository, so this list isn't filtered by type."
	}
	counts = map[string]int{"": len(rows)}
	var kept []catalogRow
	for _, row := range rows {
		if row.Type != "" {
			counts[row.Type]++
		}
		if row.Type == t {
			kept = append(kept, row)
		}
	}
	return kept, counts, ""
}

// sortRows puts rows, which arrive A–Z, in the order by. The created and
// tag-count orders load every row's details first; if that takes too long
// the rows stay A–Z and the returned note says so.
func (s *Server) sortRows(ctx context.Context, rows []catalogRow, by string) string {
	switch by {
	case "name-desc":
		slices.Reverse(rows)
		return ""
	case "created", "tags":
	default:
		return ""
	}
	err := fetchAll(ctx, len(rows), func(ctx context.Context, i int) {
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
		if by == "tags" {
			return cmp.Compare(b.Tags, a.Tags)
		}
		return newestFirst(a.Created, b.Created)
	})
	return ""
}

// catalogRow answers a row fragment. A failure only marks the row, so the
// link to the repository stays usable.
func (s *Server) catalogRow(w http.ResponseWriter, r *http.Request) {
	row := catalogRow{Repo: r.URL.Query().Get("repo")}
	if err := s.rowDetails(r.Context(), &row); err != nil {
		slog.WarnContext(r.Context(), "catalog row details", "repo", row.Repo, "err", err)
		row.Failed = true
	}
	s.fragment(w, r, "repo-row", row)
}

// rowDetails fills in row's tag count, then the description, platforms and
// created date of its latest (or newest) tag.
func (s *Server) rowDetails(ctx context.Context, row *catalogRow) error {
	tags, err := s.Src.Tags(ctx, row.Repo)
	if err != nil {
		return err
	}
	row.Tags, row.Loaded = len(tags), true
	tag := newestTag(tags)
	if tag == "" {
		if len(tags) > 0 { // only cosign tags: signatures kept apart from their images
			row.Type = "signature"
		}
		return nil
	}
	m, err := s.Src.Resolve(ctx, row.Repo, tag)
	if err != nil {
		return err
	}
	row.Helm, row.Type, row.Platforms = m.Kind == registry.KindHelm, typeOf(m.Kind), len(m.Platforms)
	if len(m.Platforms) == 0 {
		return nil
	}
	p := m.Platforms[0]
	for _, pl := range m.Platforms {
		if pl.OS == "linux" && pl.Arch == "amd64" {
			p = pl
			break
		}
	}
	cfg, err := s.Src.Config(ctx, row.Repo, p.Config)
	if err != nil {
		return err
	}
	row.Created, row.Description = cfg.Created, cfg.Labels["org.opencontainers.image.description"]
	return nil
}

// newestTag picks the tag a repository is summarised by: "latest", else the
// first in natural sort order. Cosign-style "sha256-…" tags are skipped, as
// they'd sort above version numbers and describe no image.
func newestTag(tags []string) string {
	var images []string
	for _, t := range tags {
		if !strings.HasPrefix(t, "sha256-") {
			images = append(images, t)
		}
	}
	if len(images) == 0 {
		return ""
	}
	format.SortTags(images)
	return images[0]
}
