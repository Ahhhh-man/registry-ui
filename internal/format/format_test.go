package format

import (
	"bytes"
	"html/template"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

func TestSize(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1229, "1.2 KB"},
		{10*1024 - 1, "10 KB"},
		{14 * 1024, "14 KB"},
		{1<<20 - 1, "1.0 MB"},
		{1 << 20, "1.0 MB"},
		{21 << 20, "21.0 MB"},
		{75916902, "72.4 MB"},
		{1<<30 - 1, "1.0 GB"},
		{3 << 30, "3.0 GB"},
		{math.MaxInt64, "8.0 EB"},
	} {
		if got := Size(tc.in); got != tc.want {
			t.Errorf("Size(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAge(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		name string
		in   time.Time
		want string
	}{
		{"zero", time.Time{}, ""},
		{"now", now, "today"},
		{"hours ago", now.Add(-23 * time.Hour), "today"},
		{"clock skew", now.Add(time.Hour), "today"},
		{"far future", time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC), "in the future"},
		{"one day", now.Add(-day), "yesterday"},
		{"two days", now.Add(-2 * day), "2 days ago"},
		{"29 days", now.Add(-29 * day), "29 days ago"},
		{"30 days", now.Add(-30 * day), "1 month ago"},
		{"95 days", now.Add(-95 * day), "3 months ago"},
		{"364 days", now.Add(-364 * day), "12 months ago"},
		{"365 days", now.Add(-365 * day), "1 year ago"},
		{"two years", now.Add(-730 * day), "2 years ago"},
		{"unix epoch", time.Unix(0, 0), "57 years ago"},
	} {
		if got := age(tc.in, now); got != tc.want {
			t.Errorf("%s: age = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDateTime(t *testing.T) {
	for _, tc := range []struct {
		in   time.Time
		want string
	}{
		{time.Time{}, ""},
		{time.Date(2026, 11, 6, 23, 30, 59, 0, time.UTC), "2026-11-06 23:30 UTC"},
		// Shown in UTC, not the server's zone.
		{time.Date(2026, 11, 7, 10, 30, 0, 0, time.FixedZone("AEDT", 3600*11)), "2026-11-06 23:30 UTC"},
		{time.Date(2999, 12, 31, 0, 0, 0, 0, time.UTC), "2999-12-31 00:00 UTC"},
		{time.Unix(0, 0), "1970-01-01 00:00 UTC"},
	} {
		if got := DateTime(tc.in); got != tc.want {
			t.Errorf("DateTime(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTime(t *testing.T) {
	at := time.Date(2020, 1, 2, 10, 30, 0, 0, time.FixedZone("AEDT", 3600*11))
	for _, tc := range []struct {
		opts []string
		want string
	}{
		{nil, `<time datetime="2020-01-01T23:30:00Z" title="2020-01-01 23:30 UTC">` + Age(at) + `</time>`},
		{[]string{"Created "}, `<time datetime="2020-01-01T23:30:00Z" title="2020-01-01 23:30 UTC">Created ` + Age(at) + `</time>`},
		// The sentence is kept beside the title, for app.js to fill in again.
		{[]string{"", `"Latest" created {}`}, `<time datetime="2020-01-01T23:30:00Z" title="&#34;Latest&#34; created 2020-01-01 23:30 UTC" data-title="&#34;Latest&#34; created {}">` + Age(at) + `</time>`},
	} {
		if got := string(Time(at, tc.opts...)); got != tc.want {
			t.Errorf("Time(%q)\n got %s\nwant %s", tc.opts, got, tc.want)
		}
	}
	if got := Time(time.Time{}, "Created "); got != "" {
		t.Errorf("zero time: got %q", got)
	}
}

func TestShortDigest(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"sha256:3f1a2b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f70", "3f1a2b4c5d6e"},
		{"sha512:abcdef0123456789", "abcdef012345"},
		{"3f1a2b4c5d6e7f80", "3f1a2b4c5d6e"},
		{"sha256:abc", "abc"},
		{"", ""},
	} {
		if got := ShortDigest(tc.in); got != tc.want {
			t.Errorf("ShortDigest(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPlatform(t *testing.T) {
	for _, tc := range []struct {
		in          registry.Platform
		full, short string
	}{
		{registry.Platform{OS: "linux", Arch: "arm64", Variant: "v8"}, "linux/arm64/v8", "arm64"},
		{registry.Platform{OS: "linux", Arch: "arm64"}, "linux/arm64", "arm64"},
		{registry.Platform{OS: "linux", Arch: "amd64"}, "linux/amd64", "amd64"},
		{registry.Platform{OS: "linux", Arch: "arm", Variant: "v7"}, "linux/arm/v7", "arm/v7"},
		{registry.Platform{OS: "windows", Arch: "amd64"}, "windows/amd64", "windows/amd64"},
	} {
		if got := Platform(tc.in); got != tc.full {
			t.Errorf("Platform(%+v) = %q, want %q", tc.in, got, tc.full)
		}
		if got := ShortPlatform(tc.in); got != tc.short {
			t.Errorf("ShortPlatform(%+v) = %q, want %q", tc.in, got, tc.short)
		}
	}
}

func TestSortTags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in, out []string
	}{
		{
			"numbers compare by value",
			[]string{"1.9", "1.10", "1.2", "1.100"},
			[]string{"1.100", "1.10", "1.9", "1.2"},
		},
		{
			"latest pinned first",
			[]string{"1.0", "latest", "zzz", "2.0"},
			[]string{"latest", "zzz", "2.0", "1.0"},
		},
		{
			"letters and numbers mixed",
			[]string{"v1.2.0", "2024.11.7", "pr-218", "v1.2.0-rc.1", "pr-9", "v1.10.0", "2024.2.1"},
			[]string{"v1.10.0", "v1.2.0-rc.1", "v1.2.0", "pr-218", "pr-9", "2024.11.7", "2024.2.1"},
		},
		{
			"leading zeros tie break consistently",
			[]string{"1.1", "1.01", "1.001"},
			[]string{"1.1", "1.01", "1.001"},
		},
		{
			"numbers longer than int64",
			[]string{"99999999999999999999", "100000000000000000000"},
			[]string{"100000000000000000000", "99999999999999999999"},
		},
		{"empty", []string{}, []string{}},
	} {
		got := slices.Clone(tc.in)
		SortTags(got)
		if !slices.Equal(got, tc.out) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.out)
		}
		// Any input order gives the same result.
		slices.Reverse(got)
		SortTags(got)
		if !slices.Equal(got, tc.out) {
			t.Errorf("%s (reversed input): got %v, want %v", tc.name, got, tc.out)
		}
	}
}

// BenchmarkSortTags sorts a long tag list in the A–Z order the registry
// lists it in, as every visit to the repository page does.
func BenchmarkSortTags(b *testing.B) {
	tags := make([]string, 5000)
	for j := range tags {
		tags[j] = registrytest.LargeTag(j)
	}
	slices.Sort(tags)
	got := make([]string, len(tags))
	b.ReportAllocs()
	for b.Loop() {
		copy(got, tags)
		SortTags(got)
	}
}

func TestFuncs(t *testing.T) {
	tmpl := template.Must(template.New("").Funcs(Funcs()).Parse(
		`{{size .Size}} {{shortDigest .Digest}} {{platform .P}} {{shortPlatform .P}} {{time .T}} {{age .T}}`))
	var b bytes.Buffer
	err := tmpl.Execute(&b, map[string]any{
		"Size":   int64(2048),
		"Digest": "sha256:0123456789abcdef",
		"P":      registry.Platform{OS: "linux", Arch: "arm64", Variant: "v8"},
		"T":      time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := b.String(), `2.0 KB 0123456789ab linux/arm64/v8 arm64 <time datetime="2020-01-02T00:00:00Z" title="2020-01-02 00:00 UTC">`; !strings.HasPrefix(got, want) || strings.HasSuffix(got, " ") {
		t.Errorf("got %q, want %q followed by an age", got, want)
	}
}
