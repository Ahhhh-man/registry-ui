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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/config"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
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
		// Real check lands with the static assets (#15).
		os.Exit(0)
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
	src = registry.NewCache(src)

	// Stub pages until #12–#14.
	stub := func(w http.ResponseWriter, _ *http.Request, ref web.Ref) { _, _ = fmt.Fprintf(w, "%+v\n", ref) }
	catalog := func(w http.ResponseWriter, r *http.Request, _ web.Ref) {
		repos, err := src.Catalog(r.Context())
		if err != nil {
			web.Error(w, r, err)
			return
		}
		_, _ = fmt.Fprintln(w, strings.Join(repos, "\n"))
	}
	tmpl, err := ui.Templates(web.FuncMap(cfg.BasePath, ui.StaticName))
	if err != nil {
		return err
	}
	h := web.New(cfg.BasePath, web.Handlers{
		Catalog: catalog, Repo: stub, Tag: stub, Digest: stub,
		Static: ui.StaticHandler(),
	})
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.Middleware(h, ui.ErrorRenderer(tmpl, cfg.Title)),
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
		// Storage mode plugs in here (#25).
		return nil, errors.New("REGISTRY_UI_STORAGE_PATH: storage mode is not supported yet")
	}
	return registry.NewAPI(cfg.RegistryURL.String(), cfg.RegistryInsecure)
}
