package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(env map[string]string) (Config, error) {
	return Load(func(k string) string { return env[k] })
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	type want struct {
		registry, pull, title, listen, base string
		insecure                            bool
		level                               slog.Level
	}
	tests := []struct {
		name string
		env  map[string]string
		want want
	}{
		{
			name: "defaults",
			env:  map[string]string{"REGISTRY_UI_REGISTRY_URL": "http://registry:5000"},
			want: want{registry: "http://registry:5000", pull: "registry:5000", title: "registry:5000", listen: ":8080", base: "/"},
		},
		{
			name: "pull host without port",
			env:  map[string]string{"REGISTRY_UI_REGISTRY_URL": "https://registry.example.com/"},
			want: want{registry: "https://registry.example.com/", pull: "registry.example.com", title: "registry.example.com", listen: ":8080", base: "/"},
		},
		{
			name: "everything set",
			env: map[string]string{
				"REGISTRY_UI_REGISTRY_URL":      "http://registry:5000",
				"REGISTRY_UI_PULL_URL":          "registry.example.com:443",
				"REGISTRY_UI_TITLE":             "My registry",
				"REGISTRY_UI_LISTEN":            "127.0.0.1:9000",
				"REGISTRY_UI_BASE_PATH":         "/registry/",
				"REGISTRY_UI_REGISTRY_INSECURE": "true",
				"REGISTRY_UI_LOG_LEVEL":         "debug",
			},
			want: want{registry: "http://registry:5000", pull: "registry.example.com:443", title: "My registry", listen: "127.0.0.1:9000", base: "/registry/", insecure: true, level: slog.LevelDebug},
		},
		{
			name: "title defaults to explicit pull host",
			env:  map[string]string{"REGISTRY_UI_REGISTRY_URL": "http://registry:5000", "REGISTRY_UI_PULL_URL": "cr.example.com"},
			want: want{registry: "http://registry:5000", pull: "cr.example.com", title: "cr.example.com", listen: ":8080", base: "/"},
		},
		{
			name: "storage mode needs no registry URL",
			env:  map[string]string{"REGISTRY_UI_STORAGE_PATH": dir, "REGISTRY_UI_PULL_URL": "cache.example.com"},
			want: want{pull: "cache.example.com", title: "cache.example.com", listen: ":8080", base: "/"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := load(tt.env)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := want{pull: c.PullURL, title: c.Title, listen: c.Listen, base: c.BasePath, insecure: c.RegistryInsecure, level: c.LogLevel}
			if c.RegistryURL != nil {
				got.registry = c.RegistryURL.String()
			}
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestLoadBasePath(t *testing.T) {
	tests := map[string]string{
		"":                "/",
		"/":               "/",
		"registry":        "/registry/",
		"/registry":       "/registry/",
		"registry/":       "/registry/",
		"/a//b/":          "/a/b/",
		"/a/../registry/": "/registry/",
	}
	for in, want := range tests {
		c, err := load(map[string]string{"REGISTRY_UI_REGISTRY_URL": "http://r:5000", "REGISTRY_UI_BASE_PATH": in})
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if c.BasePath != want {
			t.Errorf("%q: got %q, want %q", in, c.BasePath, want)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// with returns a valid environment with one variable overridden.
	with := func(k, v string) map[string]string {
		return map[string]string{"REGISTRY_UI_REGISTRY_URL": "http://registry:5000", k: v}
	}

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"registry URL missing", map[string]string{}, "REGISTRY_UI_REGISTRY_URL: required"},
		{"registry URL no scheme", with("REGISTRY_UI_REGISTRY_URL", "registry:5000"), "REGISTRY_UI_REGISTRY_URL:"},
		{"registry URL bad scheme", with("REGISTRY_UI_REGISTRY_URL", "ftp://registry"), "REGISTRY_UI_REGISTRY_URL:"},
		{"registry URL no host", with("REGISTRY_UI_REGISTRY_URL", "http://"), "REGISTRY_UI_REGISTRY_URL:"},
		{"pull URL with scheme", with("REGISTRY_UI_PULL_URL", "https://registry.example.com"), "REGISTRY_UI_PULL_URL:"},
		{"pull URL with path", with("REGISTRY_UI_PULL_URL", "registry.example.com/x"), "REGISTRY_UI_PULL_URL:"},
		{"pull URL missing in storage mode", map[string]string{"REGISTRY_UI_STORAGE_PATH": t.TempDir()}, "REGISTRY_UI_PULL_URL: required"},
		{"listen without port", with("REGISTRY_UI_LISTEN", "8080"), "REGISTRY_UI_LISTEN:"},
		{"base path with query", with("REGISTRY_UI_BASE_PATH", "/r?x=1"), "REGISTRY_UI_BASE_PATH:"},
		{"insecure not a bool", with("REGISTRY_UI_REGISTRY_INSECURE", "yes"), "REGISTRY_UI_REGISTRY_INSECURE:"},
		{"log level unknown", with("REGISTRY_UI_LOG_LEVEL", "loud"), "REGISTRY_UI_LOG_LEVEL:"},
		{"storage path missing", with("REGISTRY_UI_STORAGE_PATH", filepath.Join(t.TempDir(), "nope")), "REGISTRY_UI_STORAGE_PATH:"},
		{"storage path is a file", with("REGISTRY_UI_STORAGE_PATH", file), "REGISTRY_UI_STORAGE_PATH:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(tt.env)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadReportsEveryError(t *testing.T) {
	_, err := load(map[string]string{
		"REGISTRY_UI_LISTEN":    "nope",
		"REGISTRY_UI_LOG_LEVEL": "loud",
	})
	if err == nil {
		t.Fatal("want an error")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 3 {
		t.Errorf("want 3 lines (registry URL, listen, log level), got:\n%v", err)
	}
}

func TestLogValueHidesSecrets(t *testing.T) {
	c, err := load(map[string]string{
		"REGISTRY_UI_REGISTRY_URL": "http://user:hunter2@registry:5000",
		"REGISTRY_UI_SECRET":       "s3cr3t-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("start", "config", c)
	out := buf.String()
	for _, s := range []string{"hunter2", "s3cr3t-key"} {
		if strings.Contains(out, s) {
			t.Errorf("log output contains %q: %s", s, out)
		}
	}
	if !strings.Contains(out, "config.secret_set=true") {
		t.Errorf("log output should say the secret is set: %s", out)
	}
}
