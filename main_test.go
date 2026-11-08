package main

import (
	"net"
	"net/http"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/config"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

func TestHealthcheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	load := func(listen, base string) config.Config {
		env := map[string]string{
			// Nothing listens on port 1: the healthcheck must not care.
			"REGISTRY_UI_REGISTRY_URL": "http://127.0.0.1:1",
			"REGISTRY_UI_LISTEN":       listen,
			"REGISTRY_UI_BASE_PATH":    base,
		}
		cfg, err := config.Load(func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	cfg := load(":"+port, "/registry")
	srv := &http.Server{Handler: web.New(cfg.BasePath, web.Handlers{Static: ui.StaticHandler()})}
	go func() { _ = srv.Serve(ln) }()

	for _, listen := range []string{":" + port, "0.0.0.0:" + port, "[::]:" + port, "127.0.0.1:" + port} {
		if err := healthcheck(load(listen, "/registry")); err != nil {
			t.Errorf("listen %q: %v", listen, err)
		}
	}
	if err := healthcheck(load(":"+port, "/")); err == nil {
		t.Error("wrong base path: want error")
	}

	_ = srv.Close()
	if err := healthcheck(cfg); err == nil {
		t.Error("after shutdown: want error")
	}
}
