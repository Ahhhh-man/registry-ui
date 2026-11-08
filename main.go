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
	// Placeholder until the router (#4) exists.
	return http.ListenAndServe(cfg.Listen, http.NotFoundHandler())
}
