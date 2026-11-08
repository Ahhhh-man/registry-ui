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
	// Placeholder until config (#3) and the router (#4) exist.
	addr := ":8080"
	slog.Info("starting registry-ui", "version", version, "addr", addr)
	return http.ListenAndServe(addr, http.NotFoundHandler())
}
