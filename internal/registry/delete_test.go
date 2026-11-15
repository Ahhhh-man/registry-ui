package registry_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

// Deleting latest takes 1.0.0, on the same digest, and the signature named
// after it; a selected signature whose image goes isn't planned twice.
func TestPlanDelete(t *testing.T) {
	r := registrytest.New(t)
	a := registry.NewCache(newAPI(t, r.URL))
	index, sig := digest(t, r.Index), digest(t, r.Signature)

	plan, err := registry.PlanDelete(t.Context(), a, registrytest.RepoIndex, []string{registrytest.TagLatest, r.SignatureTag, registrytest.TagLatest, "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Images) != 1 {
		t.Fatalf("images = %+v, want one", plan.Images)
	}
	img := plan.Images[0]
	if img.Digest != index || !slices.Equal(img.Tags, []string{registrytest.TagVersion, registrytest.TagLatest}) || !slices.Equal(img.Selected, []string{registrytest.TagLatest}) || img.Whole() {
		t.Errorf("image = %+v", img)
	}
	if len(img.Signatures) != 1 || img.Signatures[0].Digest != sig || !slices.Equal(img.Signatures[0].Tags, []string{r.SignatureTag}) {
		t.Errorf("signatures = %+v", img.Signatures)
	}
	if !slices.Equal(plan.Missing, []string{"gone"}) {
		t.Errorf("missing = %v", plan.Missing)
	}
	if got := plan.Tags(); !slices.Equal(got, []string{registrytest.TagVersion, registrytest.TagLatest, r.SignatureTag}) {
		t.Errorf("tags = %v", got)
	}
	if got := plan.Digests(); !slices.Equal(got, []string{index, sig}) {
		t.Errorf("digests = %v", got)
	}

	// A signature selected on its own is its own image, with nothing else.
	plan, err = registry.PlanDelete(t.Context(), a, registrytest.RepoIndex, []string{r.SignatureTag})
	if err != nil || len(plan.Images) != 1 || plan.Images[0].Digest != sig || len(plan.Images[0].Signatures) != 0 || !plan.Images[0].Whole() {
		t.Errorf("signature alone: %+v, %v", plan, err)
	}

	// A dangling tag has no digest to delete it by.
	plan, err = registry.PlanDelete(t.Context(), a, registrytest.RepoDangling, []string{registrytest.TagLatest, registrytest.TagVersion})
	if err != nil || len(plan.Images) != 1 || !slices.Equal(plan.Missing, []string{registrytest.TagLatest}) {
		t.Errorf("dangling: %+v, %v", plan, err)
	}
}

// Deleting by digest takes every tag on it, then its signature.
func TestApplyDeletesSharedTagsAndSignatures(t *testing.T) {
	r := registrytest.New(t)
	a := newAPI(t, r.URL)
	plan, err := registry.PlanDelete(t.Context(), a, registrytest.RepoIndex, []string{registrytest.TagVersion})
	if err != nil {
		t.Fatal(err)
	}
	results := plan.Apply(t.Context(), a)
	if len(results) != 2 || results[0].Signature || !results[1].Signature || results[0].Err != nil || results[1].Err != nil {
		t.Fatalf("results = %+v", results)
	}
	if tags, err := a.Tags(t.Context(), registrytest.RepoIndex); err != nil || len(tags) != 0 {
		t.Errorf("tags left = %v, %v", tags, err)
	}
	// Already gone counts as deleted.
	if results := plan.Apply(t.Context(), a); len(results) != 2 || results[0].Err != nil {
		t.Errorf("again: %+v", results)
	}
}

// A failed image keeps its signature, and the rest still go.
func TestApplyPartialFailure(t *testing.T) {
	r := registrytest.New(t)
	a := newAPI(t, r.URL)
	r.RefuseDelete(registrytest.RepoIndex, digest(t, r.Index), http.StatusInternalServerError)
	plan, err := registry.PlanDelete(t.Context(), a, registrytest.RepoIndex, []string{registrytest.TagLatest})
	if err != nil {
		t.Fatal(err)
	}
	results := plan.Apply(t.Context(), a)
	if len(results) != 1 || !errors.Is(results[0].Err, registry.ErrUnavailable) {
		t.Fatalf("results = %+v, want the image failed and its signature not tried", results)
	}
	if tags, _ := a.Tags(t.Context(), registrytest.RepoIndex); len(tags) != 3 {
		t.Errorf("tags left = %v, want all three", tags)
	}

	dated := digest(t, r.Dated)
	r.RefuseDelete(registrytest.RepoUndated, dated, http.StatusForbidden)
	plan, err = registry.PlanDelete(t.Context(), a, registrytest.RepoUndated, []string{registrytest.TagDated, registrytest.TagLabelled})
	if err != nil {
		t.Fatal(err)
	}
	results = plan.Apply(t.Context(), a)
	if len(results) != 2 || results[0].Digest != dated || !errors.Is(results[0].Err, registry.ErrForbidden) || results[1].Err != nil {
		t.Errorf("results = %+v, want dated forbidden and labelled deleted", results)
	}
}

func TestCanDelete(t *testing.T) {
	r := registrytest.New(t)
	if err := newAPI(t, r.URL).CanDelete(t.Context(), registrytest.RepoIndex); err != nil {
		t.Errorf("deletes enabled: %v", err)
	}
	if tags, _ := newAPI(t, r.URL).Tags(t.Context(), registrytest.RepoIndex); len(tags) != 3 {
		t.Errorf("asking deleted something: %v", tags)
	}

	a := newAPI(t, registrytest.NewDeleteDisabled(t).URL)
	if err := a.CanDelete(t.Context(), registrytest.RepoIndex); !errors.Is(err, registry.ErrDeleteDisabled) {
		t.Errorf("deletes disabled: %v", err)
	}
	if err := a.Delete(t.Context(), registrytest.RepoIndex, digest(t, r.Index)); !errors.Is(err, registry.ErrDeleteDisabled) {
		t.Errorf("delete with deletes disabled: %v", err)
	}

	a = newAPI(t, registrytest.NewWithBasicAuth(t, "alice", "secret").URL)
	if err := a.CanDelete(t.Context(), registrytest.RepoIndex); !errors.Is(err, registry.ErrUnauthorized) {
		t.Errorf("anonymous: %v", err)
	}
	if err := a.CanDelete(registry.WithCredentials(t.Context(), "alice", "secret"), registrytest.RepoIndex); err != nil {
		t.Errorf("signed in: %v", err)
	}

	r.RefuseDelete("", "", http.StatusForbidden)
	if err := registry.NewCache(newAPI(t, r.URL)).CanDelete(t.Context(), registrytest.RepoIndex); !errors.Is(err, registry.ErrForbidden) {
		t.Errorf("forbidden through the cache: %v", err)
	}

	storage, err := registry.NewStorage(registrytest.NewStorage(t).StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.NewCache(storage).CanDelete(t.Context(), registrytest.RepoIndex); !errors.Is(err, registry.ErrDeleteDisabled) {
		t.Errorf("storage: %v", err)
	}
}
