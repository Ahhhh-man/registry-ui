// Command registry-ui serves a web UI for a CNCF Distribution registry.
//
// Usage:
//
//	registry-ui [serve]     run the web server (default)
//	registry-ui healthcheck exit 0 if the server is healthy
//
// Built with -tags fake, it serves built-in sample data instead of a real
// registry, which is handy for UI work. REGISTRY_UI_REGISTRY_URL must still
// be set, but is not used.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/config"
	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/pages"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/session"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

var version = "dev"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			slog.Error("server stopped", "err", err)
			os.Exit(1)
		}
	case "healthcheck":
		cfg, err := config.Load(os.Getenv)
		if err == nil {
			err = healthcheck(cfg)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "unhealthy: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "usage: %s [serve|healthcheck]\n", os.Args[0])
		os.Exit(2)
	}
}

func serve() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration:\n%v\n", err)
		os.Exit(1)
	}
	slog.SetLogLoggerLevel(cfg.LogLevel)
	slog.Info("starting registry-ui", "version", version, "config", cfg)
	src, err := newSource(cfg)
	if err != nil {
		return err
	}
	sessions, err := session.New(cfg.Secret, cfg.BasePath)
	if err != nil {
		return err
	}
	// Login checks credentials with the source itself, never the cache.
	check, _ := src.(interface{ Check(context.Context) error })
	challenged, _ := src.(interface{ Challenged() bool })
	src = registry.NewCache(src)

	funcs := web.FuncMap(cfg.BasePath, ui.StaticName)
	maps.Copy(funcs, format.Funcs())
	tmpl, err := ui.Templates(funcs)
	if err != nil {
		return err
	}
	p := &pages.Server{Src: src, Templates: tmpl, Title: cfg.Title, Version: version, PullHost: cfg.PullURL, BasePath: cfg.BasePath, Sessions: sessions, Storage: cfg.StoragePath != "", TagSort: cfg.TagSort, TagsPerPage: cfg.TagsPerPage, CatalogSort: cfg.CatalogSort}
	if check != nil {
		p.Check = check.Check
	}
	if challenged != nil {
		p.Challenged = challenged.Challenged
	}
	handlers := p.Handlers()
	handlers.Static = ui.StaticHandler()
	h := web.New(cfg.BasePath, handlers)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.Middleware(h, ui.ErrorRenderer(tmpl, p.Header)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // above the 10 s upstream timeout, with room for a few calls
		IdleTimeout:       2 * time.Minute,
	}
	return srv.ListenAndServe()
}

// newSource builds the Source the pages read from. In builds tagged fake,
// fake.go swaps in sample data.
var newSource = func(cfg config.Config) (registry.Source, error) {
	if cfg.StoragePath != "" {
		slog.Warn("storage mode: the registry's auth is not consulted, so anyone who can reach this UI can see every repository; put it behind your own auth if that matters")
		return registry.NewStorage(cfg.StoragePath)
	}
	return registry.NewAPI(cfg.RegistryURL.String(), cfg.RegistryInsecure)
}

// healthcheck fetches one embedded static asset from the UI's own listener.
// It never talks to the registry, so a registry outage doesn't restart the UI.
func healthcheck(cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + cfg.BasePath + "-/static/" + ui.StaticName("htmx.min.js")
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(url)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return nil
}
