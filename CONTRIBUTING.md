# Contributing

Bug reports, small fixes and documentation are always welcome. For anything bigger, please open an issue first so we can agree on the approach before you spend time on it.

## Dev setup

You need [Nix](https://nixos.org) with flakes. The dev shell has Go, the linters, the Tailwind CLI and [Task](https://taskfile.dev), at the same versions CI uses.

```sh
nix develop
task dev     # serves on :8080, templates and CSS reload on refresh
task test
task lint
task build   # ./registry-ui
task image   # builds the container image with Nix and loads it into Docker
```

With [direnv](https://direnv.net) and [nix-direnv](https://github.com/nix-community/nix-direnv), run `direnv allow` once and the dev shell loads whenever you `cd` into the repo. The dev shell also installs the repo's git hooks.

`task dev` needs `REGISTRY_UI_REGISTRY_URL` pointing at a registry. To work on the UI without one, build with `-tags fake`. That serves built-in sample data: images, Helm charts, signatures, a multi-arch index with missing platforms, and so on. The URL still has to be set, but it isn't used:

```sh
REGISTRY_UI_REGISTRY_URL=http://unused go run -tags fake .
```

No Nix? The `Dockerfile` builds an equivalent image: `docker build -t registry-ui .`. The published image is the Nix one, and you'll need Go and the Tailwind CLI yourself to work on the code.

## CSS

The CSS is built from `web/tailwind.css` into `internal/ui/static/app.css` with the Tailwind standalone CLI (`task css`, or watched by `task dev`). The output is committed, so building and running the UI needs only Go, with no Node or Tailwind. CI checks that the committed file is up to date, so run `task css` before you commit a template or style change.

## Before you push

CI runs all of these, so running them first saves a round trip:

```sh
gofumpt -l .          # prints nothing when everything is formatted
task lint
go test -race ./...
govendor --check      # the Nix vendor hash matches go.mod
task css              # and commit app.css if it changed
```

CI also builds the binary and the image, fails if the image is over 20 MB, and runs smoke tests against a real `registry:3`: anonymous, htpasswd, and a pull-through cache in storage mode that must never contact its upstream.

## Footprint and benchmarks

The UI has three budgets, listed in the README's [Footprint](README.md#footprint) section: the image size, idle memory, and no work while idle. `task budget` checks all three on Linux in about two minutes. Run it if you touch the cache, anything that holds data between requests, or anything that starts a goroutine or timer.

The benchmarks cover the paths that grow with a registry's size: the catalog at up to 10,000 repositories in each view, sort and filter, a repository with 5,000 tags, the tag page, tag sorting and a cache hit. Besides time and allocations, they report the gzipped page size and how many calls per visit got past the cache. That last one doesn't depend on your machine, so it shows when a page has outgrown the cache.

```sh
task bench                                   # all of them, about two minutes
task bench -- -bench Catalog                 # just some
```

Each run is saved to `bench-<commit>-<time>.txt`, which git ignores. To compare a change, run `task bench -- -count 6` before and after, then:

```sh
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e old.txt new.txt
```

CI runs every benchmark once to keep them working, but doesn't look at timings.

## Conventions

- One change per pull request, linked to its issue. Pull requests are squash-merged.
- Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org): `feat(ui): …`, `fix(registry): …`, `perf(web): …`, `docs: …`, `ci: …`, `chore: …`. Write the body as a few short paragraphs saying what was wrong and what changed, and end it with `Closes #N`. `git log` has plenty of examples.
- `.gitignore` is a whitelist. It ignores everything and then allows named paths, so a new top-level file or folder has to be added there or git won't see it.
- Think twice before adding a dependency to `go.mod`. A few lines of our own usually beat a module. If you do add one, `govendor` updates the Nix vendor hash.
- Errors from the registry go through `web.Error`, so they become the right page and are logged in one place.
- For tests, `registrytest` has real registry servers (anonymous, basic auth, and on-disk storage, including large generated ones), and the source tests run against both API mode and storage mode. Prefer them to mocks.
