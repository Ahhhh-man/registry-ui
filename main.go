// Command registry-ui serves a web UI for a CNCF Distribution registry.
//
// Usage:
//
//	registry-ui [serve]     run the web server (default)
//	registry-ui healthcheck exit 0 if the server is healthy
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/Ahhhh-man/registry-ui/internal/config"
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
	// Stub pages until #12–#14.
	stub := func(w http.ResponseWriter, _ *http.Request, ref web.Ref) { _, _ = fmt.Fprintf(w, "%+v\n", ref) }
	h := web.New(cfg.BasePath, web.Handlers{
		Catalog: stub, Repo: stub, Tag: stub, Digest: stub,
		Static: ui.StaticHandler(),
	})
	return http.ListenAndServe(cfg.Listen, h)
}
