package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Storage is a Source that reads a registry's filesystem storage directly,
// without any network calls. It is meant for pull-through caches, whose API
// would ask the upstream for tag lists and fetch uncached tags. It only
// reads, so the storage can be mounted read-only. Credentials are ignored.
//
// Only content that is actually on disk is shown: a digest counts when its
// link and its blob both exist, and an index's platforms whose manifests
// aren't cached are marked Missing. A tag is listed as long as its link names
// a digest, like the registry lists it, but resolves only when that manifest
// is on disk too.
type Storage struct {
	root string // <storage root>/docker/registry/v2
}

var _ Source = (*Storage)(nil)

// NewStorage returns a Source for the registry storage at root, the folder
// the registry has at /var/lib/registry. An empty folder is accepted, since
// the registry only creates its layout on the first push.
func NewStorage(root string) (*Storage, error) {
	s := &Storage{root: filepath.Join(root, "docker", "registry", "v2")}
	if fi, err := os.Stat(s.root); err == nil && fi.IsDir() {
		return s, nil
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) > 0 {
		return nil, fmt.Errorf("storage path %q: not a registry storage root (no docker/registry/v2 inside)", root)
	}
	return s, nil
}

// Catalog implements Source.
func (s *Storage) Catalog(ctx context.Context) ([]string, error) {
	top := filepath.Join(s.root, "repositories")
	repos := []string{}
	err := filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == top {
				return fs.SkipAll // nothing pushed yet
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.IsDir() || !strings.HasPrefix(d.Name(), "_") {
			return nil
		}
		// Like Distribution: a folder holding _manifests is a repository, and
		// _-prefixed folders are its data, never nested repositories.
		if d.Name() == "_manifests" {
			if repo, err := filepath.Rel(top, filepath.Dir(path)); err == nil && repoName.MatchString(filepath.ToSlash(repo)) {
				repos = append(repos, filepath.ToSlash(repo))
			}
		}
		return fs.SkipDir
	})
	if err != nil {
		return nil, s.fail("catalog", err)
	}
	slices.Sort(repos)
	return repos, nil
}

// Tags implements Source. A repository in the catalog without a tags
// folder, such as one only ever pushed by digest, has no tags; a name that
// isn't in the catalog is not found.
func (s *Storage) Tags(_ context.Context, repo string) ([]string, error) {
	if !repoName.MatchString(repo) {
		return nil, fmt.Errorf("repository %s: %w", repo, ErrNotFound)
	}
	entries, err := os.ReadDir(s.repoPath(repo, "_manifests", "tags"))
	if errors.Is(err, fs.ErrNotExist) {
		if _, serr := os.Stat(s.repoPath(repo, "_manifests")); serr == nil {
			return []string{}, nil
		}
	}
	if err != nil {
		return nil, s.fail("tags of "+repo, err)
	}
	tags := []string{}
	for _, e := range entries {
		if !tagName.MatchString(e.Name()) {
			continue
		}
		if _, err := s.link(repo, e.Name()); err == nil {
			tags = append(tags, e.Name())
		} else if !errors.Is(err, ErrNotFound) {
			return nil, s.fail("tags of "+repo, err)
		}
	}
	return tags, nil
}

// Resolve implements Source.
func (s *Storage) Resolve(_ context.Context, repo, ref string) (Manifest, error) {
	m, err := s.resolve(repo, ref)
	if err != nil {
		return Manifest{}, s.fail("manifest "+repo+":"+ref, err)
	}
	return m, nil
}

func (s *Storage) resolve(repo, ref string) (Manifest, error) {
	if !repoName.MatchString(repo) {
		return Manifest{}, ErrNotFound
	}
	digest, tag := ref, ""
	if !digestRef.MatchString(ref) {
		if !tagName.MatchString(ref) {
			return Manifest{}, ErrNotFound
		}
		tag = ref
		var err error
		if digest, err = s.tag(repo, tag); err != nil {
			return Manifest{}, err
		}
	}
	raw, err := s.manifest(repo, digest)
	if err != nil {
		return Manifest{}, err
	}
	mt := mediaType(raw)
	m := Manifest{Digest: digest, MediaType: string(mt)}

	if mt.IsIndex() {
		idx, err := v1.ParseIndexManifest(bytes.NewReader(raw))
		if err != nil {
			return Manifest{}, err
		}
		m.Kind = KindIndex
		if sigTag.MatchString(tag) {
			m.Kind = KindSignature
			return m, nil
		}
		m.Platforms = []Platform{}
		for _, d := range idx.Manifests {
			if !d.MediaType.IsImage() || isAttestation(d) {
				continue
			}
			pl := indexed(d)
			raw, err := s.manifest(repo, d.Digest.String())
			switch {
			case errors.Is(err, ErrNotFound):
				pl.Missing = true // a pull-through cache only holds the platforms someone pulled
			case err != nil:
				return Manifest{}, err
			default:
				mf, err := v1.ParseManifest(bytes.NewReader(raw))
				if err != nil {
					return Manifest{}, err
				}
				pl.Config, pl.Size, pl.Created = mf.Config.Digest.String(), size(mf), annotated(mf.Annotations, idx.Annotations)
			}
			m.Platforms = append(m.Platforms, pl)
		}
		return m, nil
	}

	mf, err := v1.ParseManifest(bytes.NewReader(raw))
	if err != nil {
		return Manifest{}, err
	}
	m.Size = size(mf)
	m.Kind = kind(tag, mf)
	if m.Kind != KindImage {
		return m, nil
	}
	pl := Platform{Digest: m.Digest, MediaType: m.MediaType, Config: mf.Config.Digest.String(), Size: m.Size, Created: annotated(mf.Annotations)}
	// A manifest fetched without its config (e.g. by `docker manifest
	// inspect` through a cache) still shows, just without its platform.
	cfg, err := s.config(repo, pl.Config)
	if err == nil {
		pl.OS, pl.Arch, pl.Variant = cfg.OS, cfg.Architecture, cfg.Variant
	} else if !errors.Is(err, ErrNotFound) {
		return Manifest{}, err
	}
	m.Platforms = []Platform{pl}
	return m, nil
}

// Config implements Source.
func (s *Storage) Config(_ context.Context, repo, digest string) (ImageConfig, error) {
	if !repoName.MatchString(repo) {
		return ImageConfig{}, fmt.Errorf("repository %s: %w", repo, ErrNotFound)
	}
	cfg, err := s.config(repo, digest)
	if err != nil {
		return ImageConfig{}, s.fail("config "+repo+"@"+digest, err)
	}
	return shown(cfg), nil
}

// TagTime implements Source with the modification time of the tag's link.
func (s *Storage) TagTime(_ context.Context, repo, tag string) (time.Time, error) {
	if !repoName.MatchString(repo) || !tagName.MatchString(tag) {
		return time.Time{}, fmt.Errorf("tag %s:%s: %w", repo, tag, ErrNotFound)
	}
	fi, err := os.Stat(s.repoPath(repo, "_manifests", "tags", tag, "current", "link"))
	if err != nil {
		return time.Time{}, s.fail("tag time of "+repo+":"+tag, err)
	}
	return fi.ModTime(), nil
}

// DiskUsage implements Source. The registry is every blob on disk, including
// any garbage collection would free; a repository is the distinct blobs it
// links to, where a link to a blob that isn't on disk counts as nothing.
func (s *Storage) DiskUsage(ctx context.Context, repo string) (int64, error) {
	var total int64
	if repo == "" {
		top := filepath.Join(s.root, "blobs", "sha256")
		err := filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && path == top {
					return fs.SkipAll // nothing pushed yet
				}
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.Name() != "data" || !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err == nil {
				total += fi.Size()
			}
			return err
		})
		if err != nil {
			return 0, s.fail("disk usage", err)
		}
		return total, nil
	}

	if !repoName.MatchString(repo) {
		return 0, fmt.Errorf("repository %s: %w", repo, ErrNotFound)
	}
	what := "disk usage of " + repo
	if _, err := os.Stat(s.repoPath(repo, "_manifests")); err != nil {
		return 0, s.fail(what, err)
	}
	seen := map[string]bool{}
	for _, dir := range [][]string{{"_layers"}, {"_manifests", "revisions"}} {
		entries, err := os.ReadDir(s.repoPath(repo, append(dir, "sha256")...))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, s.fail(what, err)
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			path, err := s.blobPath(repo, "sha256:"+e.Name(), dir...)
			if errors.Is(err, ErrNotFound) || seen[path] {
				continue
			}
			if err != nil {
				return 0, s.fail(what, err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				return 0, s.fail(what, err)
			}
			seen[path] = true
			total += fi.Size()
		}
	}
	return total, nil
}

// tag returns the digest a tag points at, if that manifest is stored.
func (s *Storage) tag(repo, tag string) (string, error) {
	digest, err := s.link(repo, tag)
	if err != nil {
		return "", err
	}
	if _, err := s.blobPath(repo, digest, "_manifests", "revisions"); err != nil {
		return "", err
	}
	return digest, nil
}

// link returns the digest a tag's link names, whether or not that manifest
// is stored: an interrupted delete can leave the link behind.
func (s *Storage) link(repo, tag string) (string, error) {
	link, err := os.ReadFile(s.repoPath(repo, "_manifests", "tags", tag, "current", "link"))
	if err != nil {
		return "", notFound(err)
	}
	digest := strings.TrimSpace(string(link))
	if !digestRef.MatchString(digest) {
		return "", ErrNotFound
	}
	return digest, nil
}

// manifest reads a manifest that repo links to.
func (s *Storage) manifest(repo, digest string) ([]byte, error) {
	return readBlob(s.blobPath(repo, digest, "_manifests", "revisions"))
}

// config reads an image config blob that repo links to.
func (s *Storage) config(repo, digest string) (*v1.ConfigFile, error) {
	raw, err := readBlob(s.blobPath(repo, digest, "_layers"))
	if err != nil {
		return nil, err
	}
	return v1.ParseConfigFile(bytes.NewReader(raw))
}

// blobPath returns the file holding the blob with the given digest, if repo
// links to it under dir and it is on disk. Distribution keeps blobs in one
// shared store and links them into each repository that uses them.
func (s *Storage) blobPath(repo, digest string, dir ...string) (string, error) {
	if !digestRef.MatchString(digest) {
		return "", ErrNotFound
	}
	hex := strings.TrimPrefix(digest, "sha256:")
	if _, err := os.Stat(s.repoPath(repo, append(dir, "sha256", hex, "link")...)); err != nil {
		return "", notFound(err)
	}
	path := filepath.Join(s.root, "blobs", "sha256", hex[:2], hex, "data")
	if _, err := os.Stat(path); err != nil {
		return "", notFound(err)
	}
	return path, nil
}

// readBlob reads a manifest or config blob found by blobPath, up to
// maxConfigSize.
func readBlob(path string, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, notFound(err)
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxConfigSize))
}

// repoPath joins a validated repository name and the parts below it.
func (s *Storage) repoPath(repo string, parts ...string) string {
	return filepath.Join(append([]string{s.root, "repositories", filepath.FromSlash(repo)}, parts...)...)
}

// notFound turns a missing file into ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// fail maps a filesystem error onto this package's errors.
func (s *Storage) fail(what string, err error) error {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	return fmt.Errorf("%s: %w: %w", what, ErrUnavailable, err)
}

// mediaType reads a manifest's media type. OCI manifests may leave it out,
// in which case an index is told apart by its manifests list.
func mediaType(raw []byte) types.MediaType {
	var probe struct {
		MediaType types.MediaType `json:"mediaType"`
		Manifests json.RawMessage `json:"manifests"`
	}
	switch _ = json.Unmarshal(raw, &probe); {
	case probe.MediaType != "":
		return probe.MediaType
	case probe.Manifests != nil:
		return types.OCIImageIndex
	}
	return types.OCIManifestSchema1
}
