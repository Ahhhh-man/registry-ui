# registry-ui

A small web UI for a self-hosted [CNCF Distribution](https://distribution.github.io/distribution/) registry (`registry:2` / `registry:3`). It answers one question: what images, tags and platforms are in my registry? You get repositories, tags, platforms, digests, sizes and created dates, plus copyable pull commands. It isn't Harbor or Zot. It doesn't push, manage users, replicate or scan. It's one static binary in an image under 20 MB that sits next to your registry and does nothing when nobody is looking at it.

## Quick start

Save this as `compose.yaml` and run `docker compose up -d`:

```yaml
services:
  registry:
    image: registry:3
    ports: ["5000:5000"]
    volumes: ["registry-data:/var/lib/registry"]

  registry-ui:
    image: ghcr.io/ahhhh-man/registry-ui:latest
    ports: ["8080:8080"]
    environment:
      REGISTRY_UI_REGISTRY_URL: http://registry:5000 # how the UI reaches the registry
      REGISTRY_UI_PULL_URL: localhost:5000 # what people type after `docker pull`
    healthcheck:
      test: ["CMD", "/registry-ui", "healthcheck"]

volumes:
  registry-data:
```

Push something and open <http://localhost:8080>:

```sh
docker pull busybox
docker tag busybox localhost:5000/busybox
docker push localhost:5000/busybox
```

The UI talks to the registry server-side. Docker clients keep talking to the registry directly, browsers never do, so there's no CORS to set up. The UI starts even if the registry isn't up yet. The healthcheck only checks the UI itself, so a registry outage won't restart it.

## Configuration

Everything is an environment variable. There are no config files or flags. An empty value counts as unset.

| Variable | Default | What it does |
|---|---|---|
| `REGISTRY_UI_REGISTRY_URL` | required | The registry's base URL, e.g. `http://registry:5000`. Optional in storage mode. |
| `REGISTRY_UI_PULL_URL` | host of `REGISTRY_UI_REGISTRY_URL` | The `host[:port]` shown in pull commands, e.g. `registry.example.com`. |
| `REGISTRY_UI_TITLE` | the pull URL | Name shown in the header and the page title. |
| `REGISTRY_UI_LISTEN` | `:8080` | Address to listen on. |
| `REGISTRY_UI_BASE_PATH` | `/` | Serve under a sub-path, e.g. `/registry/`. |
| `REGISTRY_UI_SECRET` | random on each start | Key for the session cookie. Set it to keep people signed in across restarts. |
| `REGISTRY_UI_REGISTRY_INSECURE` | `false` | Skip TLS verification when talking to the registry. |
| `REGISTRY_UI_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `REGISTRY_UI_STORAGE_PATH` | unset | Read the registry's storage folder directly instead of its API. See [Pull-through caches](#pull-through-caches). |

Invalid values stop the UI at startup with one line per problem.

### TLS and reverse proxies

The UI serves plain HTTP. Put TLS on your reverse proxy. Pass the original `Host` (or `X-Forwarded-Host`) and `X-Forwarded-Proto` through, so sign-in works and the session cookie is marked `Secure`. Caddy does this by default:

```
registry.example.com {
	reverse_proxy registry-ui:8080
}
```

To serve the UI under a sub-path, set `REGISTRY_UI_BASE_PATH=/registry/` and forward the path unchanged. Don't strip the prefix: the UI expects to see it.

```
example.com {
	handle /registry/* {
		reverse_proxy registry-ui:8080
	}
}
```

### Registries with a private CA

The image trusts the usual public CAs. For a registry signed by your own CA, mount the CA certificate into a folder and point `SSL_CERT_DIR` at it, or point `SSL_CERT_FILE` at a bundle that includes it (this replaces the built-in bundle). For a quick test, `REGISTRY_UI_REGISTRY_INSECURE=true` skips verification altogether.

## Authentication

The UI has no users of its own. It mirrors the registry:

- If the registry allows anonymous reads, there's no sign-in at all.
- If the registry asks for credentials, the UI shows a sign-in form, checks what you enter against the registry, and forwards it on every request. Basic auth and token servers both work.
- Your credentials are sealed (AES-GCM) in an `HttpOnly` cookie that lasts 12 hours. The UI keeps nothing on disk.
- Without `REGISTRY_UI_SECRET`, a new key is picked on every start and everyone is signed out on restart. Set it to any long random string, e.g. `openssl rand -hex 32`.

## Pull-through caches

If your registry is a pull-through cache (`REGISTRY_PROXY_REMOTEURL`), use storage mode. Browsing a proxy through its API does the wrong thing: it asks the upstream for every tag list, pulls and stores any tag you click that isn't cached yet, and spends your upstream rate limit doing it. You'd see the upstream's tags, not your cache.

In storage mode the UI reads the registry's storage folder directly, mounted read-only, and never sends the registry or the upstream a single request. You see exactly what's cached. Multi-arch images only list the platforms someone has actually pulled.

```yaml
services:
  registry:
    image: registry:3
    ports: ["5000:5000"]
    environment:
      REGISTRY_PROXY_REMOTEURL: https://registry-1.docker.io
    volumes: ["cache-data:/var/lib/registry"]

  registry-ui:
    image: ghcr.io/ahhhh-man/registry-ui:latest
    ports: ["8080:8080"]
    environment:
      REGISTRY_UI_STORAGE_PATH: /var/lib/registry
      REGISTRY_UI_PULL_URL: localhost:5000 # required when REGISTRY_UI_REGISTRY_URL is unset
    volumes: ["cache-data:/var/lib/registry:ro"]
    healthcheck:
      test: ["CMD", "/registry-ui", "healthcheck"]

volumes:
  cache-data:
```

Pull something through the cache (`docker pull localhost:5000/library/busybox`) and it shows up in the UI.

**Storage mode skips the registry's auth.** Anyone who can reach the UI sees everything in the cache. If that matters, put the UI behind your reverse proxy's auth.

Limits:

- Only the registry's `filesystem` storage driver works. S3, GCS, Azure and the rest don't.
- The UI must see the same folder the registry writes to: same host, or a volume both containers mount.
- An empty folder is fine. The registry creates its layout on the first pull.

## Supported registries

[CNCF Distribution](https://distribution.github.io/distribution/) (`registry:2` and `registry:3`) is the one that's supported and tested. Others that implement the OCI distribution API and `/v2/_catalog` should work, but are untested. GHCR, Docker Hub and ECR restrict or lack `_catalog`, so don't expect them to work.

## No telemetry

The UI never talks to anything except the registry you configure. No telemetry, no update checks, no CDNs. Fonts, scripts and styles are all embedded in the binary.

## Development

You need [Nix](https://nixos.org) with flakes. The dev shell has Go, the linters, the Tailwind CLI and [Task](https://taskfile.dev).

```sh
nix develop
task dev     # serves on :8080, templates and CSS reload on refresh
task test
task lint
task build   # ./registry-ui
task image   # builds the container image with Nix and loads it into Docker
```

`task dev` needs `REGISTRY_UI_REGISTRY_URL` pointing at a registry. To work on the UI without one, build with `-tags fake`, which serves built-in sample data (the URL must be set but isn't used):

```sh
REGISTRY_UI_REGISTRY_URL=http://unused go run -tags fake .
```

The CSS is built with the Tailwind standalone CLI (`task css`, or watched by `task dev`) from `web/tailwind.css` into `internal/ui/static/app.css`. The output is committed, so building and running the UI needs only Go, no Node or Tailwind. CI checks the committed file is up to date, so run `task css` before you commit a template or style change.

No Nix? The `Dockerfile` builds an equivalent image: `docker build -t registry-ui .`. The published image is the Nix one.

## License

[MIT](LICENSE)
