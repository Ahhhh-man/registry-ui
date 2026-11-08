package ui

import (
	"html"
	"html/template"
	"maps"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// partialCases render every partial and variant with representative data,
// and say what each must produce.
var partialCases = []struct {
	name, call string
	want       []string
}{
	{
		"button", `{{template "button" (dict "Label" "Clear" "Icon" "x")}}`,
		[]string{`<button type="button" class="`, "hover:border-faint", `<svg class="icon"`, "Clear</button>"},
	},
	{
		"button submit", `{{template "button" (dict "Label" "Sign in" "Type" "submit")}}`,
		[]string{`<button type="submit"`, "Sign in</button>"},
	},
	{
		"button link", `{{template "button" (dict "Label" "Retry" "Href" "/x?a=1&b=2")}}`,
		[]string{`<a href="/x?a=1&amp;b=2" class="`, "Retry</a>"},
	},
	{
		"button danger", `{{template "button" (dict "Label" "Delete" "Icon" "trash-2" "Variant" "danger")}}`,
		[]string{"text-danger-fg", "Delete</button>"},
	},
	{
		"button icon", `{{template "button" (dict "Label" "Log out" "Icon" "log-out" "Variant" "icon")}}`,
		[]string{`aria-label="Log out" title="Log out"`, "size-9", "</svg></button>"},
	},
	{
		"button action", `{{template "button" (dict "Label" "Copy names" "Action" "names")}}`,
		[]string{`<button type="button" data-action="names" class="`, "Copy names</button>"},
	},
	{
		"copy", `{{template "copy" (dict "Text" "sha256:abc" "Label" "Copy digest")}}`,
		[]string{`<button type="button" data-copy="sha256:abc" aria-label="Copy digest"`, `<span data-copy-label aria-live="polite">Copy</span>`, "data-copied:"},
	},
	{
		"copy-field", `{{template "copy-field" "docker pull registry.example.com/library/nginx:1.27"}}`,
		[]string{"font-mono", `>docker pull registry.example.com/library/nginx:1.27</code>`, `data-copy="docker pull registry.example.com/library/nginx:1.27"`, `aria-label="Copy command"`},
	},
	{
		"code-block", `{{template "code-block" (dict "Label" "Copy pull command" "Command" "docker pull registry.example.com/library/nginx")}}`,
		[]string{"bg-code-bg", "wrap-anywhere", `data-copy="docker pull registry.example.com/library/nginx" aria-label="Copy pull command"`, "bg-accent", "text-accent-fg"},
	},
	{
		"tabs", `{{template "tabs" (dict "Label" "Repository" "Tabs" (list (dict "Label" "Tags" "Icon" "tag" "Href" "/library/nginx" "Count" 42 "Current" true) (dict "Label" "Overview" "Icon" "book-open" "Href" "/library/nginx?tab=overview")))}}`,
		[]string{`<nav aria-label="Repository"`, `<a href="/library/nginx" aria-current="page"`, `<a href="/library/nginx?tab=overview" class=`, ">42</span>", "aria-[current=page]:border-accent"},
	},
	{
		"segmented", `{{template "segmented" (dict "Label" "View" "Name" "view" "Options" (list (dict "Value" "cards" "Label" "Cards" "Icon" "layers" "Pressed" true) (dict "Value" "list" "Label" "List" "Icon" "list" "Disabled" true)))}}`,
		[]string{`<form method="get" role="group" aria-label="View"`, `<button type="submit" name="view" value="cards" aria-pressed="true"`, `value="list" aria-pressed="false" disabled`, "aria-pressed:bg-panel"},
	},
	{
		"type-chips", `{{template "type-chips" (list (dict "Value" "" "Label" "All" "Href" "/" "Count" 8) (dict "Value" "image" "Label" "Images" "Href" "/?type=image" "Count" 7 "Current" true) (dict "Value" "helm" "Label" "Helm charts" "Href" "/?type=helm" "Count" -1) (dict "Value" "signature" "Label" "Signatures" "Href" "/?type=signature" "Count" -1))}}`,
		[]string{
			`<nav aria-label="Content type"`, `<a href="/" class=`, `All<span class="tabular-nums opacity-70">8</span></a>`,
			`<a href="/?type=image" aria-current="true" class=`, "bg-accent-soft text-accent-text", `<svg class="icon size-3 text-info-fg"`, "Helm charts</a>",
		},
	},
	{
		"type-chips helm", `{{template "type-chips" (list (dict "Value" "helm" "Label" "Helm charts" "Href" "/?type=helm" "Count" 1 "Current" true))}}`,
		[]string{`aria-current="true"`, "bg-info-bg text-info-fg"},
	},
	{
		"type-chips signature", `{{template "type-chips" (list (dict "Value" "signature" "Label" "Signatures" "Href" "/?type=signature" "Count" 0 "Current" true))}}`,
		[]string{`aria-current="true"`, "bg-warn-bg text-warn-fg", ">0</span>"},
	},
	{
		"filter", `<form>{{template "filter" (dict "Name" "q" "Value" "1.27" "Placeholder" "Filter tags" "Label" "Filter tags")}}</form>`,
		[]string{`<input type="search" name="q" value="1.27" placeholder="Filter tags" aria-label="Filter tags"`, `data-hotkey="/"`},
	},
	{"badge", `{{template "badge" (dict "Label" "linux/amd64")}}`, []string{"bg-panel text-muted", ">linux/amd64</span>"}},
	{"badge accent", `{{template "badge" (dict "Label" "latest" "Variant" "accent")}}`, []string{"bg-accent-soft text-accent-text", ">latest</span>"}},
	{"badge signature", `{{template "badge" (dict "Label" "signature" "Variant" "signature")}}`, []string{"bg-warn-bg text-warn-fg", "</svg>signature</span>"}},
	{"badge helm", `{{template "badge" (dict "Label" "helm chart" "Variant" "helm")}}`, []string{"bg-info-bg text-info-fg", "</svg>helm chart</span>"}},
	{"pill", `{{template "pill" (dict "Icon" "hash" "Text" "3f2a9c1b7e44" "Mono" true)}}`, []string{"rounded-sm border", `<span class="truncate font-mono">3f2a9c1b7e44</span>`}},
	{
		"pill copy", `{{template "pill" (dict "Icon" "hash" "Text" "3f2a9c1b7e44" "Mono" true "Copy" "sha256:3f2a")}}`,
		[]string{`data-copy="sha256:3f2a" aria-label="Copy digest"`, "group-data-copied:block"},
	},
	{
		"pill links", `{{template "pill" (dict "Icon" "link" "Text" "Same as" "Title" "hover" "Links" (list (dict "Label" "latest" "Href" "/n:latest") (dict "Label" "1.27" "Href" "/n:1.27")))}}`,
		[]string{`<span title="hover" class=`, `<span class="min-w-0">Same as <a href="/n:latest" class="`, `>latest</a>, <a href="/n:1.27"`},
	},
	{
		"fact-card", `{{template "fact-card" (dict "ID" "platform-linux-amd64" "Current" true "Icon" "cpu" "Title" "linux/amd64" "Size" "69.0 MB" "Text" "About it." "Facts" (list (dict "Label" "Digest" "Value" "3f2a9c1b7e44" "Mono" true "Title" "sha256:3f2a" "Href" "/n@sha256:3f2a") (dict "Label" "Type" "Value" "OCI image manifest")) "Copies" (list (dict "Text" "sha256:3f2a" "Caption" "Copy digest")))}}`,
		[]string{
			`<article id="platform-linux-amd64" aria-current="true"`, "aria-[current=true]:border-accent", "</svg><span", ">linux/amd64</span></h3>", ">69.0 MB</span>", "About it.</p>",
			`<dt class="text-muted">Digest</dt>`, `title="sha256:3f2a"><a href="/n@sha256:3f2a"`, ">3f2a9c1b7e44</a></dd>", ">OCI image manifest</dd>",
			`data-copy="sha256:3f2a"`, `aria-live="polite">Copy digest</span>`,
		},
	},
	{
		"breadcrumbs", `{{template "breadcrumbs" (dict "Trail" (list (dict "Label" "Repositories" "Href" "/") (dict "Label" "library")) "Current" "nginx")}}`,
		[]string{`<nav aria-label="Breadcrumb">`, `<a href="/" class="hover:text-text">Repositories</a>`, "<span>library</span>", `aria-current="page">nginx</li>`},
	},
	{"repo-tile", `{{template "repo-tile" (dict "Repo" "library/nginx")}}`, []string{`aria-hidden="true"`, TileClass("library/nginx"), "size-10", ">N</span>"}},
	{"repo-tile helm", `{{template "repo-tile" (dict "Repo" "charts/podinfo" "Helm" true "Large" true)}}`, []string{"size-16", `<svg class="icon size-8"`}},
	{
		"page-header", `{{template "page-header" (dict "Repo" "library/nginx" "Prefix" "library/" "Title" "nginx" "Description" "Official build of Nginx." "Badges" (list (dict "Label" "latest" "Variant" "accent")) "Pills" (list (dict "Icon" "tag" "Text" "42 tags") (dict "Icon" "cpu" "Text" "4 platforms")))}}`,
		[]string{"<h1 ", `<span class="font-medium text-faint">library/</span>nginx</span>`, "size-16", "Official build of Nginx.", ">42 tags</span>", ">latest</span>"},
	},
	{"page-header plain", `{{template "page-header" (dict "Title" "Repositories")}}`, []string{"Repositories</span>"}},
	{"card", `<div class="{{template "card"}}"><div>Summary</div><div>Table</div></div>`, []string{`class="divide-y divide-row-border`, "*:px-4"}},
	{
		"panel", `<section class="{{template "panel"}}">{{template "panel-header" (dict "Icon" "book-open" "Title" "About")}}<p>Text</p></section>`,
		[]string{`<section class="rounded-md border border-border bg-surface p-5">`, "<h2 ", "</svg></span>About</h2>"},
	},
	{
		"table", `<table class="{{template "table"}}"><thead><tr><th>OS / Arch</th><th>Size</th></tr></thead><tbody><tr><td>linux/amd64</td><td>72.4 MB</td></tr></tbody></table>`,
		[]string{"[&_th]:uppercase", "<th>OS / Arch</th>"},
	},
	{
		"table selectable", `<table class="{{template "table" "selectable"}}"><tbody><tr><td><input type="checkbox" aria-label="Select 1.27" checked></td><td>1.27</td></tr><tr><td><input type="checkbox" aria-label="Select 1.26"></td><td>1.26</td></tr></tbody></table>`,
		[]string{"[&_tbody_tr:has(:checked)]:bg-accent-soft"},
	},
	{"stat", `{{template "stat" (dict "Icon" "package" "Label" "Repositories" "Value" 128)}}`, []string{">128</div>", "Repositories</div>"}},
	{"stat loading", `<div aria-busy="true">{{template "stat" (dict "Icon" "tag" "Label" "Tags")}}</div>`, []string{"motion-safe:animate-pulse w-12"}},
	{"skeleton", `<p aria-busy="true">{{template "skeleton" "w-1/2"}}</p>`, []string{`<span aria-hidden="true" class="inline-block`, "w-1/2"}},
	{
		"empty", `{{template "empty" (dict "Title" "No tags match “zzz”" "Href" "/library/nginx" "Link" "Clear the filter")}}`,
		[]string{"border-dashed", "No tags match “zzz”", `<a href="/library/nginx"`, "Clear the filter</a>"},
	},
	{
		"error-state", `{{template "error-state" (dict "Title" "Can't reach the registry" "Text" "registry:5000 refused the connection." "Retry" "/")}}`,
		[]string{"bg-danger-bg", "Can&#39;t reach the registry", `<a href="/" class=`, "Retry</a>"},
	},
	{"list-header", `{{template "list-header" (dict "Title" "All repositories")}}`, []string{"<h2 ", "All repositories</h2>"}},
	{
		"list-header sort", `{{template "list-header" (dict "Title" "All repositories" "Sort" (dict "Label" "Name A–Z" "Options" (list (dict "Label" "Name A–Z" "Href" "/" "Current" true) (dict "Label" "Most tags" "Href" "/?sort=tags"))))}}`,
		[]string{
			`<details data-menu class="group relative">`, `<span class="sr-only">Sort by </span>Name A–Z<svg`, `<ul class="absolute right-0 `,
			`<li><a href="/" aria-current="true" class=`, `<li><a href="/?sort=tags" class=`, ">Most tags</a></li>",
		},
	},
	{
		"repo-row loading", `{{template "repo-row" (dict "Repo" "library/nginx" "Namespace" "library/" "Name" "nginx")}}`,
		[]string{`<a href="/library/nginx" data-row aria-busy="true" hx-get="/?fragment=row&amp;repo=library/nginx" hx-trigger="intersect once" hx-swap="outerHTML"`, `<span class="font-medium text-faint">library/</span>nginx</div>`, "animate-pulse w-1/2"},
	},
	{
		"repo-row loaded", `{{template "repo-row" (dict "Repo" "charts/podinfo" "Namespace" "charts/" "Name" "podinfo" "Loaded" true "Helm" true "Tags" 1 "Age" "2 days ago" "Date" "2024-11-06")}}`,
		[]string{`data-row data-tags="1"`, "No description label", "</svg>Helm chart</span>", "</svg>1 tag</span>", `title="Latest created 2024-11-06"`, "</svg>2 days ago</span>"},
	},
	{"repo-row failed", `{{template "repo-row" (dict "Repo" "x/y" "Namespace" "x/" "Name" "y" "Failed" true)}}`, []string{"Couldn't load details"}},
	{"note", `{{template "note" "Layers and build history come later."}}`, []string{`role="note"`, "bg-accent-soft", "Layers and build history come later."}},
	{"warning", `{{template "warning" "2 more tags will also go."}}`, []string{`role="note"`, "bg-warn-bg", "2 more tags will also go."}},
}

// serverFuncs are the template funcs the server wires up, at base; the
// page templates need them all to parse.
func serverFuncs(base string, hashed func(string) string) template.FuncMap {
	funcs := web.FuncMap(base, hashed)
	maps.Copy(funcs, format.Funcs())
	return funcs
}

// parseCases parses the templates plus a "case-<name>" template per case.
func parseCases(t testing.TB, funcs template.FuncMap) *template.Template {
	t.Helper()
	tpl, err := parseTemplates(os.DirFS("."), funcs)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range partialCases {
		if _, err := tpl.New("case-" + c.name).Parse(c.call); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	return tpl
}

func TestPartials(t *testing.T) {
	tpl := parseCases(t, serverFuncs("/", nil))
	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"light", "dark"} {
		for _, c := range partialCases {
			var body strings.Builder
			if err := tpl.ExecuteTemplate(&body, "case-"+c.name, nil); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			var page strings.Builder
			err := tpl.ExecuteTemplate(&page, "layout", Page{Title: "registry.example.com", Theme: theme, Body: template.HTML(body.String())}) //nolint:gosec // our own render
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			s := page.String()
			if !strings.Contains(s, `<html lang="en" class="`+theme+`">`) {
				t.Errorf("%s: not rendered in %s", c.name, theme)
			}
			for _, w := range c.want {
				if !strings.Contains(body.String(), w) {
					t.Errorf("%s (%s): lacks %s in\n%s", c.name, theme, w, body.String())
				}
			}
			checkCSP(t, c.name, body.String())
			checkClasses(t, c.name, body.String(), css)
		}
	}
}

// inline matches markup the Content-Security-Policy would block.
var inline = regexp.MustCompile(`style=|<style|<script|hx-on|javascript:|\son[a-z]+=`)

// checkCSP fails on markup the Content-Security-Policy would block.
func checkCSP(t *testing.T, name, s string) {
	t.Helper()
	if m := inline.FindString(s); m != "" {
		t.Errorf("%s: contains %s", name, m)
	}
}

var classAttr = regexp.MustCompile(`class="([^"]*)"`)

// checkClasses fails on a class with no rule in app.css: with Tailwind's
// default palette and spacing cleared, a typo or an off-scale value such as
// p-7 would otherwise just render unstyled.
func checkClasses(t *testing.T, name, s string, css []byte) {
	t.Helper()
	markers := map[string]bool{"group": true}
	for _, m := range classAttr.FindAllStringSubmatch(s, -1) {
		for _, c := range strings.Fields(html.UnescapeString(m[1])) {
			// The name must end there, so size-1 can't pass as .size-10.
			rule := regexp.MustCompile(`\.` + regexp.QuoteMeta(cssEscape(c)) + `[^-_a-zA-Z0-9\\]`)
			if markers[c] || rule.Match(css) {
				continue
			}
			t.Errorf("%s: class %q has no CSS; run task css, or it's off-system", name, c)
		}
	}
}

// cssEscape escapes a class name the way Tailwind writes it in a selector.
func cssEscape(c string) string {
	return regexp.MustCompile(`[^-_a-zA-Z0-9]`).ReplaceAllString(c, `\$0`)
}

func TestTileLetter(t *testing.T) {
	for repo, want := range map[string]string{"library/nginx": "N", "redis": "R", "a/b/élan": "É", "x/": "?"} {
		if got := TileLetter(repo); got != want {
			t.Errorf("TileLetter(%q) = %q, want %q", repo, got, want)
		}
	}
}

func TestDict(t *testing.T) {
	m, err := Dict("A", 1, "B", "two")
	if err != nil || m["A"] != 1 || m["B"] != "two" || len(m) != 2 {
		t.Errorf("Dict = %v, %v", m, err)
	}
	if _, err := Dict("A"); err == nil {
		t.Error("odd arguments: no error")
	}
	if _, err := Dict(1, 2); err == nil {
		t.Error("non-string key: no error")
	}
}
