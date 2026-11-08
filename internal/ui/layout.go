package ui

import (
	"errors"
	"fmt"
	"hash/fnv"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

// Page is the data for the "layout" template, which wraps a rendered page
// body in the document, header and theme.
type Page struct {
	Title     string        // registry title, in the header and <title>
	PageTitle string        // optional, shown before Title in <title>
	Theme     string        // Theme(r)
	Query     string        // current repo search, echoed in the search box
	Body      template.HTML // the page itself, rendered by its own template
}

// Theme returns the class for <html> from the theme cookie that app.js sets:
// "dark", "light", or "" to follow the OS setting.
func Theme(r *http.Request) string {
	c, err := r.Cookie("theme")
	if err != nil || (c.Value != "dark" && c.Value != "light") {
		return ""
	}
	return c.Value
}

// TileClass returns the background class for a repository's letter tile,
// one of eight colours, fixed per name.
func TileClass(repo string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(repo)) // never fails
	return "bg-tile-" + strconv.Itoa(int(h.Sum32()%8)+1)
}

// TileLetter returns the upper-case first letter of a repository's name,
// ignoring its namespace: "library/nginx" gives "N".
func TileLetter(repo string) string {
	name := repo[strings.LastIndexByte(repo, '/')+1:]
	for _, r := range name {
		return string(unicode.ToUpper(r))
	}
	return "?"
}

// Dict builds a map from key, value pairs, so a template can hand a partial
// several values: {{template "pill" (dict "Icon" "tag" "Text" "12 tags")}}.
func Dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("ui: dict needs key, value pairs")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("ui: dict key %v isn't a string", kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

// List returns its arguments as a slice, for partials that take several
// items: (list (dict "Label" "Tags") (dict "Label" "Overview")).
func List(v ...any) []any { return v }

// builtins are available to every template; Templates adds them.
var builtins = template.FuncMap{
	"icon":       Icon,
	"tileClass":  TileClass,
	"tileLetter": TileLetter,
	"dict":       Dict,
	"list":       List,
}
