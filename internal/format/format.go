package format

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
)

// Funcs returns the helpers as template functions.
func Funcs() template.FuncMap {
	return template.FuncMap{
		"size":          Size,
		"age":           Age,
		"time":          Time,
		"shortDigest":   ShortDigest,
		"platform":      Platform,
		"shortPlatform": ShortPlatform,
	}
}

// Size formats a byte count in binary units (1 KB = 1024 bytes). The labels
// say KB and MB rather than KiB and MiB because that's what docker and most
// registry tools print. Megabytes and up always show one decimal; kilobytes
// show one below 10 KB, e.g. "1.2 KB", "14 KB", "72.4 MB".
func Size(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n) / 1024
	units := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	i := 0
	// Step up before rounding would print "1024 KB" or "1024.0 MB".
	for v >= 1023.5 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 && v >= 9.95 {
		return fmt.Sprintf("%.0f KB", v)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// Age says how long ago t was: "today", "yesterday", "5 days ago",
// "3 months ago" or "2 years ago". A zero time gives "".
func Age(t time.Time) string { return age(t, time.Now()) }

func age(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	days := int(now.Sub(t) / (24 * time.Hour))
	switch {
	case days <= -1:
		return "in the future"
	case days < 1:
		return "today"
	case days == 1:
		return "yesterday"
	case days < 30:
		return fmt.Sprintf("%d days ago", days)
	case days < 365:
		return ago((days+15)/30, "month")
	default:
		return ago((days+182)/365, "year")
	}
}

func ago(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

// DateTime formats t as a date and time in UTC, with the zone named, e.g.
// "2026-11-06 23:30 UTC". A zero time gives "".
func DateTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// Time draws t as a <time> element: its age as the text and the exact time
// on hover, in UTC until app.js rewrites it to the viewer's own zone. opts
// are a prefix for the text ("Created ") and then a sentence for the hover
// text, with {} where the time goes ("Latest created {}"). A zero time
// gives "".
func Time(t time.Time, opts ...string) template.HTML {
	if t.IsZero() {
		return ""
	}
	opts = append(opts, "", "")
	esc := template.HTMLEscapeString
	title, slot := DateTime(t), ""
	if s := opts[1]; s != "" {
		title, slot = strings.Replace(s, "{}", title, 1), ` data-title="`+esc(s)+`"`
	}
	return template.HTML(`<time datetime="` + t.UTC().Format(time.RFC3339) + `" title="` + esc(title) + `"` + slot + `>` + esc(opts[0]+Age(t)) + `</time>`) //nolint:gosec // everything variable is escaped
}

// ShortDigest returns the first 12 hex characters of a digest, without its
// "sha256:" prefix.
func ShortDigest(d string) string {
	_, hex, ok := strings.Cut(d, ":")
	if !ok {
		hex = d
	}
	return hex[:min(12, len(hex))]
}

// Platform formats p as "os/arch" or "os/arch/variant".
func Platform(p registry.Platform) string {
	s := p.OS + "/" + p.Arch
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// ShortPlatform is the compact form for chips: linux is implied and arm64's
// default v8 variant is dropped, so "linux/arm64/v8" becomes "arm64" while
// "linux/arm/v7" stays "arm/v7" and "windows/amd64" keeps its OS.
func ShortPlatform(p registry.Platform) string {
	if p.Arch == "arm64" && p.Variant == "v8" {
		p.Variant = ""
	}
	return strings.TrimPrefix(Platform(p), "linux/")
}
