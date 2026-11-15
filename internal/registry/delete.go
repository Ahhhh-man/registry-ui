package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sync/errgroup"
)

// planLimit caps the digest lookups PlanDelete makes at once.
const planLimit = 8

// Deleter is a Source that can delete manifests. The API implements it, and
// so does a Cache in front of one; storage mode only reads, so Storage
// doesn't.
type Deleter interface {
	Source

	// CanDelete reports whether ctx's identity may delete manifests in repo:
	// nil if so, ErrDeleteDisabled if the registry refuses deletes, and
	// ErrUnauthorized or ErrForbidden if this identity may not. It deletes
	// nothing.
	CanDelete(ctx context.Context, repo string) error

	// Delete deletes the manifest with the given digest from repo. The
	// registry removes every tag pointing at it with it. Its blobs stay on
	// disk until the registry's garbage collection runs.
	Delete(ctx context.Context, repo, digest string) error
}

// ErrDeleteDisabled means the registry refuses to delete manifests: it
// answered 405 UNSUPPORTED, as Distribution does without
// REGISTRY_STORAGE_DELETE_ENABLED=true and as every pull-through cache does.
var ErrDeleteDisabled = errors.New("the registry doesn't allow deletes")

// probeDigest is a digest no manifest has. Deleting it is how CanDelete
// asks: a registry that allows deletes answers 404, one that doesn't 405.
var probeDigest = "sha256:" + strings.Repeat("0", 64)

// DeletePlan is what deleting some tags of a repository deletes. The
// registry deletes by digest, so every other tag on a selected tag's digest
// goes too, and so do the cosign signature, attestation and SBOM tags named
// after that digest.
type DeletePlan struct {
	Repo string
	// Images has one entry per digest a selected tag points at, in the
	// order the tags were selected.
	Images []PlannedImage
	// Missing are selected tags that can't be deleted: they're no longer in
	// the repository, or their manifest is gone, so there's no digest to
	// delete them by.
	Missing []string
}

// PlannedImage is one digest a DeletePlan deletes.
type PlannedImage struct {
	Digest string
	// Tags are every tag pointing at Digest, sorted; Selected are those of
	// them that were asked for.
	Tags, Selected []string
	// Signatures are the manifests of the cosign tags named after Digest,
	// deleted once Digest is.
	Signatures []PlannedSignature
}

// PlannedSignature is a cosign signature, attestation or SBOM manifest.
type PlannedSignature struct {
	Digest string
	Tags   []string // every tag pointing at Digest, sorted
}

// Whole reports whether every tag on the image was selected, so deleting it
// takes nothing that wasn't asked for besides its signatures.
func (p PlannedImage) Whole() bool { return len(p.Selected) == len(p.Tags) }

// Tags returns every tag the plan deletes, signatures included, in the
// order they go.
func (p DeletePlan) Tags() []string {
	var tags []string
	for _, img := range p.Images {
		tags = append(tags, img.Tags...)
		for _, s := range img.Signatures {
			tags = append(tags, s.Tags...)
		}
	}
	return tags
}

// Digests returns every digest the plan deletes, in the order they go.
func (p DeletePlan) Digests() []string {
	var ds []string
	for _, img := range p.Images {
		ds = append(ds, img.Digest)
		for _, s := range img.Signatures {
			ds = append(ds, s.Digest)
		}
	}
	return ds
}

// PlanDelete works out what deleting the selected tags of repo deletes. It
// looks up the digest of every tag in the repository, bypassing any cache,
// since a tag pushed a moment ago may have joined a selected one. A tag
// whose manifest is gone can't share a digest, so it is left out, or
// listed in Missing if selected; any other failed lookup fails the plan,
// as a tag it couldn't see might share a selected digest.
func PlanDelete(ctx context.Context, src Source, repo string, selected []string) (DeletePlan, error) {
	if c, ok := src.(*Cache); ok {
		src = c.src
	}
	plan := DeletePlan{Repo: repo}
	asked := map[string]bool{}
	selected = slices.DeleteFunc(slices.Clone(selected), func(t string) bool {
		dup := asked[t]
		asked[t] = true
		return dup
	})
	tags, err := src.Tags(ctx, repo)
	if err != nil {
		return plan, err
	}
	digests := make([]string, len(tags))
	var g errgroup.Group
	g.SetLimit(planLimit)
	for i, t := range tags {
		g.Go(func() error {
			d, err := src.Digest(ctx, repo, t)
			if errors.Is(err, ErrNotFound) {
				return nil // dangling: digests[i] stays ""
			}
			digests[i] = d
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return plan, err
	}

	byDigest := map[string][]string{}
	digestOf := map[string]string{}
	for i, t := range tags {
		if d := digests[i]; d != "" {
			byDigest[d] = append(byDigest[d], t)
			digestOf[t] = d
		}
	}
	for _, ts := range byDigest {
		slices.Sort(ts)
	}
	// subject is the digest a cosign tag is named after.
	subject := func(tag string) string {
		if !sigTag.MatchString(tag) {
			return ""
		}
		return "sha256:" + tag[len("sha256-"):len("sha256-")+64]
	}

	images := map[string]int{} // digest → index in plan.Images
	add := func(tag string) {
		d := digestOf[tag]
		i, ok := images[d]
		if !ok {
			i, images[d] = len(plan.Images), len(plan.Images)
			plan.Images = append(plan.Images, PlannedImage{Digest: d, Tags: byDigest[d]})
		}
		plan.Images[i].Selected = append(plan.Images[i].Selected, tag)
	}
	// Images first, then any cosign tag selected on its own: one whose image
	// is going anyway goes with it, as its signature.
	for _, sigs := range []bool{false, true} {
		for _, t := range selected {
			if (subject(t) != "") != sigs {
				continue
			}
			if _, ok := digestOf[t]; !ok {
				plan.Missing = append(plan.Missing, t)
				continue
			}
			if _, going := images[subject(t)]; sigs && going {
				continue
			}
			add(t)
		}
	}
	for i, img := range plan.Images {
		seen := map[string]bool{}
		for _, t := range tags {
			d, ok := digestOf[t]
			if subject(t) != img.Digest || !ok || seen[d] {
				continue
			}
			seen[d] = true
			plan.Images[i].Signatures = append(plan.Images[i].Signatures, PlannedSignature{Digest: d, Tags: byDigest[d]})
		}
	}
	return plan, nil
}

// DeleteResult is what happened to one digest of a DeletePlan.
type DeleteResult struct {
	Digest string
	Tags   []string // the tags that went with it, or would have
	// Signature is set for a signature's manifest, deleted after its image.
	Signature bool
	// Err is why it wasn't deleted, nil if it was. A signature whose image
	// couldn't be deleted isn't tried, and has no result.
	Err error
}

// Apply deletes the plan's digests with d, one at a time, each image
// before its signatures, so a signature never goes without its image. It
// carries on after a failure and returns a result per digest it tried. A
// digest that is already gone counts as deleted.
func (p DeletePlan) Apply(ctx context.Context, d Deleter) []DeleteResult {
	var results []DeleteResult
	del := func(digest string, tags []string, sig bool) bool {
		err := d.Delete(ctx, p.Repo, digest)
		if errors.Is(err, ErrNotFound) {
			err = nil
		}
		results = append(results, DeleteResult{Digest: digest, Tags: tags, Signature: sig, Err: err})
		return err == nil
	}
	for _, img := range p.Images {
		if !del(img.Digest, img.Tags, false) {
			continue
		}
		for _, s := range img.Signatures {
			del(s.Digest, s.Tags, true)
		}
	}
	return results
}

// CanDelete implements Deleter. It asks the registry to delete a digest no
// manifest has, which deletes nothing.
func (a *API) CanDelete(ctx context.Context, repo string) error {
	if err := a.Delete(ctx, repo, probeDigest); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// Delete implements Deleter, signing in as ctx's identity with a token
// scoped to delete.
func (a *API) Delete(ctx context.Context, repo, digest string) error {
	r, err := a.repo(repo)
	if err != nil {
		return err
	}
	if !digestRef.MatchString(digest) {
		return fmt.Errorf("manifest %s@%s: %w", repo, digest, ErrNotFound)
	}
	ctx, c := a.call(ctx)
	err = remote.Delete(r.Digest(digest), remote.WithContext(ctx), remote.WithAuth(c.auth), remote.WithTransport(a.transport),
		// As pullers do: fail fast rather than retry.
		remote.WithRetryStatusCodes(), remote.WithRetryPredicate(func(error) bool { return false }))
	if err != nil {
		return c.fail("delete "+repo+"@"+digest, err)
	}
	return nil
}
