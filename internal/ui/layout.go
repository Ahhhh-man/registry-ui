package ui

import (
	"hash/fnv"
	"html/template"
	"net/http"
	"strconv"
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

// builtins are available to every template; Templates adds them.
var builtins = template.FuncMap{
	"icon":      Icon,
	"tileClass": TileClass,
}
