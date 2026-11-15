package config

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Config is the validated configuration. Load fills every field, applying
// defaults, so callers never need to check for empty values (except
// RegistryURL in storage mode, and Secret and StoragePath, which are optional).
type Config struct {
	// RegistryURL is the registry's base URL. It is nil only in storage mode
	// when REGISTRY_UI_REGISTRY_URL is unset.
	RegistryURL *url.URL
	// PullURL is the host[:port] shown in docker pull commands.
	PullURL string
	// Title is the name shown in the header and <title>.
	Title string
	// Listen is the address the HTTP server listens on.
	Listen string
	// BasePath is the sub-path the UI is served under. It always starts and
	// ends with "/".
	BasePath string
	// Secret is key material for the session cookie. Empty means random per
	// process.
	Secret string
	// RegistryInsecure skips TLS verification to the registry.
	RegistryInsecure bool
	// RegistryHeaders are sent with every request to the registry in API
	// mode, from REGISTRY_UI_REGISTRY_HEADER_<NAME>. Nil when none are set.
	RegistryHeaders http.Header
	LogLevel        slog.Level
	// StoragePath, when set, is an existing directory holding the registry's
	// filesystem storage (storage mode).
	StoragePath string
	// TagSort is the order a repository's tags are listed in when the URL
	// doesn't say, one of tagSorts.
	TagSort string
	// TagsPerPage is how many tags a repository's page lists.
	TagsPerPage int
	// CatalogSort is the order the catalog is in when the URL doesn't say,
	// one of catalogSorts.
	CatalogSort string
}

// tagSorts are the values of REGISTRY_UI_TAG_SORT: the repo page's ?sort=
// values, plus "version" for the order that has none.
var tagSorts = []string{"version", "version-asc", "name", "created", "updated", "size"}

// catalogSorts are the values of REGISTRY_UI_CATALOG_SORT: the catalog's
// ?sort= values, plus "name" for the order that has none.
var catalogSorts = []string{"name", "name-desc", "created", "updated", "tags"}

// headerPrefix starts the variables that set RegistryHeaders.
const headerPrefix = "REGISTRY_UI_REGISTRY_HEADER_"

// Load reads the REGISTRY_UI_* variables from environ (usually os.Environ()),
// a list of "key=value" strings. An empty value counts as unset. Every invalid
// value is reported in the returned error, one per line.
func Load(environ []string) (Config, error) {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	getenv := func(k string) string { return env[k] }

	var errs []error
	bad := func(name, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{name}, args...)...))
	}

	c := Config{
		PullURL:     getenv("REGISTRY_UI_PULL_URL"),
		Title:       getenv("REGISTRY_UI_TITLE"),
		Listen:      getenv("REGISTRY_UI_LISTEN"),
		Secret:      getenv("REGISTRY_UI_SECRET"),
		StoragePath: getenv("REGISTRY_UI_STORAGE_PATH"),
	}

	if v := c.StoragePath; v != "" {
		if fi, err := os.Stat(v); err != nil {
			bad("REGISTRY_UI_STORAGE_PATH", "%v", err)
		} else if !fi.IsDir() {
			bad("REGISTRY_UI_STORAGE_PATH", "%q is not a directory", v)
		}
	}

	registryURL := getenv("REGISTRY_UI_REGISTRY_URL")
	switch v := registryURL; {
	case v == "" && c.StoragePath == "":
		bad("REGISTRY_UI_REGISTRY_URL", "required, e.g. http://registry:5000")
	case v != "":
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("REGISTRY_UI_REGISTRY_URL", "%q is not an http(s) URL with a host, e.g. http://registry:5000", v)
		} else {
			c.RegistryURL = u
		}
	}

	if c.PullURL == "" {
		if c.RegistryURL != nil {
			c.PullURL = c.RegistryURL.Host
		} else if registryURL == "" && c.StoragePath != "" {
			bad("REGISTRY_UI_PULL_URL", "required in storage mode when REGISTRY_UI_REGISTRY_URL is unset")
		}
	} else if u, err := url.Parse("//" + c.PullURL); err != nil || u.Host != c.PullURL {
		bad("REGISTRY_UI_PULL_URL", "%q is not a host[:port] (no scheme or path), e.g. registry.example.com", c.PullURL)
	}

	if c.Title == "" {
		c.Title = c.PullURL
	}

	if c.Listen == "" {
		c.Listen = ":8080"
	} else if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		bad("REGISTRY_UI_LISTEN", "%q is not a host:port address, e.g. :8080", c.Listen)
	}

	c.BasePath = "/"
	if v := getenv("REGISTRY_UI_BASE_PATH"); v != "" {
		if strings.ContainsAny(v, "?#") {
			bad("REGISTRY_UI_BASE_PATH", "%q must be a plain path, e.g. /registry/", v)
		} else if p := path.Clean("/" + v); p != "/" {
			c.BasePath = p + "/"
		}
	}

	if v := getenv("REGISTRY_UI_REGISTRY_INSECURE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			bad("REGISTRY_UI_REGISTRY_INSECURE", "%q is not a boolean (true or false)", v)
		}
		c.RegistryInsecure = b
	}

	if v := getenv("REGISTRY_UI_LOG_LEVEL"); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			bad("REGISTRY_UI_LOG_LEVEL", "%q is not one of debug, info, warn or error", v)
		}
	}

	for _, k := range slices.Sorted(maps.Keys(env)) {
		v := env[k]
		suffix, ok := strings.CutPrefix(k, headerPrefix)
		if !ok || v == "" {
			continue
		}
		if suffix == "" || strings.ContainsFunc(suffix, func(r rune) bool { return !isTokenRune(r) }) {
			bad(k, "the part after %s must be letters, digits and underscores, e.g. %sX_API_KEY", headerPrefix, headerPrefix)
			continue
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			bad(k, "the value must be on one line")
			continue
		}
		if c.RegistryHeaders == nil {
			c.RegistryHeaders = http.Header{}
		}
		c.RegistryHeaders.Set(strings.ReplaceAll(suffix, "_", "-"), v)
	}

	c.TagSort = "version"
	if v := getenv("REGISTRY_UI_TAG_SORT"); v != "" {
		c.TagSort = v
		if !slices.Contains(tagSorts, v) {
			bad("REGISTRY_UI_TAG_SORT", "%q is not one of version, version-asc, name, created, updated or size", v)
		} else if v == "updated" && c.StoragePath == "" {
			bad("REGISTRY_UI_TAG_SORT", "%q needs REGISTRY_UI_STORAGE_PATH: only the registry's storage records when a tag was updated", v)
		}
	}

	c.TagsPerPage = 100
	if v := getenv("REGISTRY_UI_TAGS_PER_PAGE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 10 || n > 500 {
			bad("REGISTRY_UI_TAGS_PER_PAGE", "%q is not a number from 10 to 500", v)
		} else {
			c.TagsPerPage = n
		}
	}

	c.CatalogSort = "name"
	if v := getenv("REGISTRY_UI_CATALOG_SORT"); v != "" {
		c.CatalogSort = v
		if !slices.Contains(catalogSorts, v) {
			bad("REGISTRY_UI_CATALOG_SORT", "%q is not one of name, name-desc, created, updated or tags", v)
		} else if v == "updated" && c.StoragePath == "" {
			bad("REGISTRY_UI_CATALOG_SORT", "%q needs REGISTRY_UI_STORAGE_PATH: only the registry's storage records when a tag was updated", v)
		}
	}

	return c, errors.Join(errs...)
}

// isTokenRune reports whether r may appear in the name part of a header
// variable: ASCII letters, digits and underscores (which become hyphens).
func isTokenRune(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// LogValue implements slog.LogValuer so the config can be logged as-is. It
// never includes the secret, any password in the registry URL or the values of
// registry headers, only their names.
func (c Config) LogValue() slog.Value {
	registry := ""
	if c.RegistryURL != nil {
		registry = c.RegistryURL.Redacted()
	}
	return slog.GroupValue(
		slog.String("registry_url", registry),
		slog.String("pull_url", c.PullURL),
		slog.String("title", c.Title),
		slog.String("listen", c.Listen),
		slog.String("base_path", c.BasePath),
		slog.Bool("secret_set", c.Secret != ""),
		slog.Bool("registry_insecure", c.RegistryInsecure),
		slog.Any("registry_headers", slices.Sorted(maps.Keys(c.RegistryHeaders))),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("storage_path", c.StoragePath),
		slog.String("tag_sort", c.TagSort),
		slog.Int("tags_per_page", c.TagsPerPage),
		slog.String("catalog_sort", c.CatalogSort),
	)
}
