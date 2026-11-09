package pages

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"
)

// sortTimeout bounds the details a sort fetches, so sorting a big registry
// ends with a note instead of hanging. A var so tests can shorten it.
var sortTimeout = 10 * time.Second

// sortChoice is one order a list can be sorted in. A page's default has
// Value "" so default URLs stay clean: the catalog's first, and whichever
// of the tags' withDefault picks.
type sortChoice struct{ Value, Label string }

var (
	// "updated" is only offered when the source has tag times; see sorts.
	catalogSorts = []sortChoice{{"", "Name A–Z"}, {"name-desc", "Name Z–A"}, {"created", "Recently created"}, {"updated", "Recently updated"}, {"tags", "Most tags"}}
	tagSorts     = []sortChoice{{"version", "Version, newest first"}, {"version-asc", "Version, oldest first"}, {"name", "Name A–Z"}, {"created", "Recently created"}, {"updated", "Recently updated"}, {"size", "Largest first"}}
)

// tagTimes reports whether the source knows when tags last changed, as
// storage mode does. A source that can't tell says so for any tag, so the
// empty one asks without touching a repository.
func (s *Server) tagTimes(ctx context.Context) bool {
	_, err := s.Src.TagTime(ctx, "", "")
	return !errors.Is(err, errors.ErrUnsupported)
}

// sorts is choices without "Recently updated" when there are no tag times.
func sorts(choices []sortChoice, tagTimes bool) []sortChoice {
	if tagTimes {
		return choices
	}
	return slices.DeleteFunc(slices.Clone(choices), func(c sortChoice) bool { return c.Value == "updated" })
}

// withDefault is choices with the one named def made the default: its Value
// is "", so its links carry no sort and its own name falls back to it.
func withDefault(choices []sortChoice, def string) []sortChoice {
	choices = slices.Clone(choices)
	for i, c := range choices {
		if c.Value == def {
			choices[i].Value = ""
		}
	}
	return choices
}

// sortMenu is the data for the "sort-menu" template.
type sortMenu struct {
	Label   string // the current order
	Options []sortOption
}

type sortOption struct {
	Label, Href string
	Current     bool
}

// pickSort returns v if it names one of choices, else "", the default.
func pickSort(choices []sortChoice, v string) string {
	for _, c := range choices {
		if c.Value == v {
			return v
		}
	}
	return ""
}

// newSortMenu lists choices with cur picked; href gives each one's link.
func newSortMenu(choices []sortChoice, cur string, href func(sort string) string) sortMenu {
	var m sortMenu
	for _, c := range choices {
		if c.Value == cur {
			m.Label = c.Label
		}
		m.Options = append(m.Options, sortOption{Label: c.Label, Href: href(c.Value), Current: c.Value == cur})
	}
	return m
}

// pageURL is path with the query kv (key, value pairs), later pairs
// overriding earlier ones and empty values left out.
func pageURL(path string, kv ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			q.Del(kv[i])
		} else {
			q.Set(kv[i], kv[i+1])
		}
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// fetchAll calls fetch for each of 0…n-1, resolveLimit at a time, and stops
// starting new calls once sortTimeout has passed, reporting that it did.
// fetch keeps its own results and errors.
func fetchAll(ctx context.Context, n int, fetch func(ctx context.Context, i int)) error {
	ctx, cancel := context.WithTimeout(ctx, sortTimeout)
	defer cancel()
	var g errgroup.Group
	g.SetLimit(resolveLimit)
	for i := range n {
		if ctx.Err() != nil {
			break
		}
		g.Go(func() error {
			fetch(ctx, i)
			return nil
		})
	}
	_ = g.Wait()
	return ctx.Err()
}

// newestFirst orders created dates newest first, with unknown (zero)
// dates last.
func newestFirst(a, b time.Time) int {
	if a.IsZero() != b.IsZero() {
		if a.IsZero() {
			return 1
		}
		return -1
	}
	return b.Compare(a)
}
