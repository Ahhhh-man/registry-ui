package pages

import (
	"context"
	"net/url"
	"time"

	"golang.org/x/sync/errgroup"
)

// sortTimeout bounds the details a sort fetches, so sorting a big registry
// ends with a note instead of hanging. A var so tests can shorten it.
var sortTimeout = 10 * time.Second

// sortChoice is one order a list can be sorted in. The first of a page's
// choices is its default, with Value "" so default URLs stay clean.
type sortChoice struct{ Value, Label string }

var (
	catalogSorts = []sortChoice{{"", "Name A–Z"}, {"name-desc", "Name Z–A"}, {"created", "Recently created"}, {"tags", "Most tags"}}
	tagSorts     = []sortChoice{{"", "Version, newest first"}, {"version-asc", "Version, oldest first"}, {"name", "Name A–Z"}, {"created", "Recently created"}, {"size", "Largest first"}}
)

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
