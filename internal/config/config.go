package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path"
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
	LogLevel         slog.Level
	// StoragePath, when set, is an existing directory holding the registry's
	// filesystem storage (storage mode).
	StoragePath string
}

// Load reads the REGISTRY_UI_* variables through getenv (usually os.Getenv).
// An empty value counts as unset. Every invalid value is reported in the
// returned error, one per line.
func Load(getenv func(string) string) (Config, error) {
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

	return c, errors.Join(errs...)
}

// LogValue implements slog.LogValuer so the config can be logged as-is. It
// never includes the secret or any password in the registry URL.
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
		slog.String("log_level", c.LogLevel.String()),
		slog.String("storage_path", c.StoragePath),
	)
}
