// Package fake is an in-memory registry.Source with realistic data, so pages
// can be built and tested before the real client is wired up. The data
// mirrors the design prototype.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
)

const (
	mediaIndex    = "application/vnd.oci.image.index.v1+json"
	mediaManifest = "application/vnd.oci.image.manifest.v1+json"

	labelDescription = "org.opencontainers.image.description"
	labelSource      = "org.opencontainers.image.source"
	labelURL         = "org.opencontainers.image.url"
	labelDocs        = "org.opencontainers.image.documentation"
)

var (
	full   = []string{"linux/amd64", "linux/arm64", "linux/arm/v7", "linux/386", "linux/ppc64le", "linux/s390x"}
	alpine = append(slices.Clone(full), "linux/arm/v6", "linux/riscv64")
	two    = []string{"linux/amd64", "linux/arm64"}
	one    = []string{"linux/amd64"}
)

// entry is one tag in the seed data: an image, a Helm chart, or an alias of
// another tag in the same repo.
type entry struct {
	platforms []string
	mb        float64
	days      int
	helm      bool
	alias     string
	// missing are platforms listed in the index whose manifests aren't in
	// the registry.
	missing []string
	// gone is a tag the registry lists but whose manifest is missing.
	gone bool
}

func img(p []string, mb float64, days int) entry { return entry{platforms: p, mb: mb, days: days} }
func helm(mb float64, days int) entry            { return entry{helm: true, mb: mb, days: days} }
func alias(tag string) entry                     { return entry{alias: tag} }
func gone() entry                                { return entry{gone: true} }

func partial(p, missing []string, mb float64, days int) entry {
	return entry{platforms: p, missing: missing, mb: mb, days: days}
}

type repoSeed struct {
	description, source string
	url, docs           string
	tags                map[string]entry
	// signed names a tag that gets a cosign "sha256-<digest>.sig" tag.
	signed string
}

var seed = map[string]repoSeed{
	"library/nginx": {
		description: "Official build of Nginx, a high-performance HTTP server and reverse proxy.",
		source:      "https://github.com/nginxinc/docker-nginx",
		url:         "https://nginx.org/",
		docs:        "https://nginx.org/en/docs/",
		signed:      "1.27.3",
		tags: map[string]entry{
			"latest": alias("1.27.3"), "mainline": alias("1.27.3"), "stable": alias("1.26.2"),
			"1.27.3": img(full, 72.4, 2), "1.27.3-alpine": img(alpine, 21.0, 2),
			"1.27.2": img(full, 71.9, 23), "1.27.2-alpine": img(alpine, 20.8, 23),
			"1.27": alias("1.27.3"), "1.26.2": img(full, 70.1, 96), "1.26.2-alpine": img(alpine, 20.1, 96),
			"1.26": alias("1.26.2"), "1.25.5": img(full, 68.3, 210),
			"1.10.3": img(one, 44.6, 2400), "1.9.15": img(one, 42.1, 2600),
		},
	},
	"library/postgres": {
		description: "The PostgreSQL object-relational database system.",
		source:      "https://github.com/docker-library/postgres",
		url:         "https://github.com/docker-library/postgres", // as docker/metadata-action sets it
		tags: map[string]entry{
			"latest": alias("17.2"), "17.2": img(full, 158.3, 9), "17.2-alpine": img(alpine, 98.4, 9),
			"17": alias("17.2"), "16.6": img(full, 151.0, 9), "16": alias("16.6"), "15.10": img(full, 147.2, 9),
		},
	},
	"library/redis": {
		description: "Redis, the in-memory data store used as a database, cache and message broker.",
		source:      "https://github.com/docker-library/redis",
		tags: map[string]entry{
			"latest": alias("7.4.1"), "7.4.1": img(full, 45.2, 30), "7.4.1-alpine": img(alpine, 16.9, 30),
			// Indexes some of whose platforms, or all, a pull-through cache
			// hasn't stored yet.
			"7.2.6":  partial(full, []string{"linux/amd64", "linux/s390x"}, 44.1, 61),
			"7.0.15": partial(two, two, 0, 400),
		},
	},
	"bitnami/kafka": {
		description: "Bitnami container image for Apache Kafka.",
		source:      "https://github.com/bitnami/containers",
		tags:        map[string]entry{"latest": alias("3.8.1"), "3.8.1": img(two, 312.5, 14), "3.7.1": img(two, 305.8, 120)},
	},
	"bitnami/minio": {
		description: "Bitnami container image for MinIO object storage.",
		source:      "https://github.com/bitnami/containers",
		tags:        map[string]entry{"latest": alias("2024.11.7"), "2024.11.7": img(two, 92.3, 3), "2024.10.29": img(two, 91.8, 12)},
	},
	"charts/podinfo": {
		tags: map[string]entry{"6.7.1": helm(0.014, 20), "6.7.0": helm(0.014, 61)},
	},
	"myteam/api": {
		description: "Internal REST API service.",
		tags: map[string]entry{
			"main": img(one, 24.6, 0), "v1.4.0": img(two, 24.1, 6), "v1.3.2": img(two, 23.8, 33), "pr-218": img(one, 24.7, 1),
			// Left behind by a delete that didn't finish.
			"v1.3.1": gone(),
		},
	},
	"myteam/web/frontend": {
		tags: map[string]entry{"main": img(one, 31.2, 0), "v2.0.0": img(one, 30.9, 15)},
	},
}

// Per-platform sizes vary a little from the amd64 size, as in real images.
var sizeFactor = map[string]float64{
	"linux/amd64": 1, "linux/arm64": .97, "linux/arm/v7": .91, "linux/arm/v6": .9,
	"linux/386": .99, "linux/ppc64le": 1.06, "linux/s390x": 1.02, "linux/riscv64": .95,
}

type repo struct {
	tags      []string
	manifests map[string]registry.Manifest // by tag and by digest
	configs   map[string]registry.ImageConfig
}

// Source is the fake registry. It is read-only after New, so it is safe for
// concurrent use.
type Source struct {
	repos map[string]*repo
}

var _ registry.Source = (*Source)(nil)

// New builds the fake registry. Created dates are relative to now, so the
// UI shows sensible ages ("2 days ago").
func New() *Source {
	now := time.Now().UTC().Truncate(time.Second)
	s := &Source{repos: map[string]*repo{}}

	for name, rs := range seed {
		r := &repo{manifests: map[string]registry.Manifest{}, configs: map[string]registry.ImageConfig{}}
		labels := map[string]string{}
		if rs.description != "" {
			labels[labelDescription] = rs.description
		}
		if rs.source != "" {
			labels[labelSource] = rs.source
		}
		if rs.url != "" {
			labels[labelURL] = rs.url
		}
		if rs.docs != "" {
			labels[labelDocs] = rs.docs
		}

		// Real tags first, so aliases can point at them.
		for tag, e := range rs.tags {
			if e.alias != "" || e.gone {
				continue
			}
			created := now.Add(-time.Duration(e.days) * 24 * time.Hour)
			m := r.add(name, tag, e, created, labels)
			r.manifests[tag] = m
		}
		for tag, e := range rs.tags {
			if e.alias != "" {
				r.manifests[tag] = r.manifests[e.alias]
			}
		}
		r.tags = slices.Collect(maps.Keys(rs.tags))
		if rs.signed != "" {
			target := r.manifests[rs.signed]
			tag := "sha256-" + strings.TrimPrefix(target.Digest, "sha256:") + ".sig"
			m := registry.Manifest{Digest: digest(name, tag), MediaType: mediaManifest, Kind: registry.KindSignature, Size: 1200}
			r.manifests[tag] = m
			r.manifests[m.Digest] = m
			r.tags = append(r.tags, tag)
		}
		s.repos[name] = r
	}
	return s
}

// add registers a non-alias tag's manifest (and any per-platform manifests
// and configs) under their digests, and returns the top-level manifest.
func (r *repo) add(name, tag string, e entry, created time.Time, labels map[string]string) registry.Manifest {
	size := func(platform string) int64 { return int64(e.mb * 1e6 * sizeFactor[platform]) }

	if e.helm {
		m := registry.Manifest{Digest: digest(name, tag), MediaType: mediaManifest, Kind: registry.KindHelm, Size: int64(e.mb * 1e6)}
		r.manifests[m.Digest] = m
		return m
	}

	platforms := make([]registry.Platform, len(e.platforms))
	for i, p := range e.platforms {
		parts := strings.SplitN(p, "/", 3)
		pl := registry.Platform{
			OS: parts[0], Arch: parts[1],
			Digest:    digest(name, tag, p),
			MediaType: mediaManifest,
			Config:    digest(name, tag, p, "config"),
			Size:      size(p),
		}
		if len(parts) == 3 {
			pl.Variant = parts[2]
		}
		if slices.Contains(e.missing, p) {
			pl.Config, pl.Size, pl.Missing = "", 0, true
		} else {
			r.configs[pl.Config] = registry.ImageConfig{Created: created, Labels: labels}
		}
		platforms[i] = pl
	}

	if len(platforms) == 1 {
		// A single-platform image: the platform manifest is the tag's manifest.
		m := registry.Manifest{Digest: platforms[0].Digest, MediaType: mediaManifest, Kind: registry.KindImage, Size: platforms[0].Size, Platforms: platforms}
		r.manifests[m.Digest] = m
		return m
	}

	m := registry.Manifest{Digest: digest(name, tag), MediaType: mediaIndex, Kind: registry.KindIndex, Platforms: platforms}
	r.manifests[m.Digest] = m
	// Each platform manifest can be opened by digest too.
	for _, pl := range platforms {
		if pl.Missing {
			continue
		}
		r.manifests[pl.Digest] = registry.Manifest{Digest: pl.Digest, MediaType: mediaManifest, Kind: registry.KindImage, Size: pl.Size, Platforms: []registry.Platform{pl}}
	}
	return m
}

func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, ":")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Check accepts any credentials, like registry.API.Check against a registry
// without auth.
func (s *Source) Check(context.Context) error { return nil }

// Catalog implements registry.Source.
func (s *Source) Catalog(context.Context) ([]string, error) {
	return slices.Sorted(maps.Keys(s.repos)), nil
}

// Tags implements registry.Source.
func (s *Source) Tags(_ context.Context, name string) ([]string, error) {
	r, err := s.repo(name)
	if err != nil {
		return nil, err
	}
	return slices.Clone(r.tags), nil
}

// Resolve implements registry.Source.
func (s *Source) Resolve(_ context.Context, name, ref string) (registry.Manifest, error) {
	r, err := s.repo(name)
	if err != nil {
		return registry.Manifest{}, err
	}
	m, ok := r.manifests[ref]
	if !ok {
		return registry.Manifest{}, fmt.Errorf("manifest %s:%s: %w", name, ref, registry.ErrNotFound)
	}
	m.Platforms = slices.Clone(m.Platforms)
	return m, nil
}

// Digest implements registry.Source.
func (s *Source) Digest(ctx context.Context, name, tag string) (string, error) {
	m, err := s.Resolve(ctx, name, tag)
	return m.Digest, err
}

// Config implements registry.Source.
func (s *Source) Config(_ context.Context, name, dgst string) (registry.ImageConfig, error) {
	r, err := s.repo(name)
	if err != nil {
		return registry.ImageConfig{}, err
	}
	c, ok := r.configs[dgst]
	if !ok {
		return registry.ImageConfig{}, fmt.Errorf("config %s@%s: %w", name, dgst, registry.ErrNotFound)
	}
	c.Labels = maps.Clone(c.Labels)
	return c, nil
}

// TagTime implements registry.Source. Like the registry API, the fake has
// no push times.
func (s *Source) TagTime(context.Context, string, string) (time.Time, error) {
	return time.Time{}, errors.ErrUnsupported
}

// DiskUsage implements registry.Source. Like the registry API, the fake
// can't tell.
func (s *Source) DiskUsage(context.Context, string) (int64, error) {
	return 0, errors.ErrUnsupported
}

func (s *Source) repo(name string) (*repo, error) {
	r, ok := s.repos[name]
	if !ok {
		return nil, fmt.Errorf("repository %s: %w", name, registry.ErrNotFound)
	}
	return r, nil
}
