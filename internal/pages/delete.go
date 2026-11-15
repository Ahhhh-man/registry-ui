package pages

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// deleteView is the data for the "delete-dialog" template: what deleting
// the selected tags takes before it happens, and what went after.
type deleteView struct {
	Repo     string
	Selected []tagChip // the selected tags that will go
	Extra    []tagChip // the other tags that go with them: same digest, or their signatures
	Missing  []tagChip // selected tags with no manifest to delete them by
	// Sent back on confirming, so the server can tell whether anything
	// changed since: the tags asked for, every tag that goes, and the
	// digests deleted.
	Tags, Gone, Digests []string
	Changed             bool   // what would go has changed since the dialog was shown
	Error               string // why nothing can be deleted

	// After deleting:
	Done     bool
	Deleted  []string // the tags that went
	Failures []deleteFailure
}

type deleteFailure struct {
	Digest string
	Tags   []tagChip
	Reason string
}

// Plural is plural for the template: "1 tag", "2 tags".
func (deleteView) Plural(n int, unit string) string { return plural(n, unit) }

// DeleteTags serves /-/delete, which only POST reaches: with the repo and
// the selected tags it answers the confirmation dialog; with confirm set as
// well, and the tags and digests that dialog showed, it deletes them unless
// that has changed meanwhile, and answers the result. Registry requests sign
// in as the session's user.
func (s *Server) DeleteTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	v := deleteView{Repo: r.PostFormValue("repo"), Tags: r.PostForm["tag"]}
	if !web.ValidRepo(v.Repo) || len(v.Tags) == 0 || slices.ContainsFunc(v.Tags, func(t string) bool { return !web.ValidTag(t) }) {
		http.Error(w, "a repository and valid tag names are required", http.StatusBadRequest)
		return
	}
	r, signedIn := s.signIn(r)
	ctx := r.Context()
	d, ok := s.Src.(registry.Deleter)
	if !ok {
		v.Error = noDelete(registry.ErrDeleteDisabled, signedIn)
		s.fragment(w, r, "delete-dialog", v)
		return
	}
	if err := d.CanDelete(ctx, v.Repo); err != nil {
		v.Error = noDelete(err, signedIn)
		s.fragment(w, r, "delete-dialog", v)
		return
	}
	plan, err := registry.PlanDelete(ctx, s.Src, v.Repo, v.Tags)
	if err != nil {
		slog.WarnContext(ctx, "plan delete", "repo", v.Repo, "err", err)
		v.Error = "Nothing was deleted: " + reason(err) + " while looking up which tags share a digest. Try again."
		s.fragment(w, r, "delete-dialog", v)
		return
	}
	v.preview(plan)
	if r.PostFormValue("confirm") == "" {
		s.fragment(w, r, "delete-dialog", v)
		return
	}
	if !sameSet(v.Gone, r.PostForm["gone"]) || !sameSet(v.Digests, r.PostForm["digest"]) {
		v.Changed = true
		s.fragment(w, r, "delete-dialog", v)
		return
	}
	v.result(ctx, plan.Apply(ctx, d))
	s.fragment(w, r, "delete-dialog", v)
}

// preview fills in what plan deletes.
func (v *deleteView) preview(plan registry.DeletePlan) {
	v.Gone, v.Digests = plan.Tags(), plan.Digests()
	for _, t := range v.Tags {
		if slices.Contains(v.Gone, t) {
			v.Selected = append(v.Selected, tagChip{t, tagLabel(t)})
		}
	}
	for _, t := range v.Gone {
		if !slices.Contains(v.Tags, t) {
			v.Extra = append(v.Extra, tagChip{t, tagLabel(t)})
		}
	}
	for _, t := range plan.Missing {
		v.Missing = append(v.Missing, tagChip{t, tagLabel(t)})
	}
}

// result fills in what deleting did.
func (v *deleteView) result(ctx context.Context, results []registry.DeleteResult) {
	v.Done = true
	for _, res := range results {
		if res.Err == nil {
			v.Deleted = append(v.Deleted, res.Tags...)
			continue
		}
		slog.WarnContext(ctx, "delete", "repo", v.Repo, "digest", res.Digest, "err", res.Err)
		f := deleteFailure{Digest: res.Digest, Reason: reason(res.Err)}
		for _, t := range res.Tags {
			f.Tags = append(f.Tags, tagChip{t, tagLabel(t)})
		}
		v.Failures = append(v.Failures, f)
	}
}

// noDelete explains, where the Delete button would be, why there isn't one.
func noDelete(err error, signedIn bool) string {
	switch {
	case errors.Is(err, registry.ErrDeleteDisabled):
		return "This registry doesn't allow deletes. It needs REGISTRY_STORAGE_DELETE_ENABLED=true, and pull-through caches never allow them."
	case (errors.Is(err, registry.ErrUnauthorized) || errors.Is(err, registry.ErrForbidden)) && signedIn:
		return "Your account can't delete tags in this repository."
	case errors.Is(err, registry.ErrUnauthorized) || errors.Is(err, registry.ErrForbidden):
		return "Sign in with an account that can delete tags to delete them here."
	}
	return "The registry didn't say whether it allows deletes, so deleting is off for now."
}

// reason says in a few words why the registry refused or failed.
func reason(err error) string {
	switch {
	case errors.Is(err, registry.ErrDeleteDisabled):
		return "the registry doesn't allow deletes"
	case errors.Is(err, registry.ErrUnauthorized), errors.Is(err, registry.ErrForbidden):
		return "your account isn't allowed to"
	}
	return "the registry failed"
}

// sameSet reports whether a and b hold the same strings, in any order.
func sameSet(a, b []string) bool {
	a, b = slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b))
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// canDelete is whether the list view offers Delete…, and if not, why.
func (s *Server) canDelete(r *http.Request, repo string) (bool, string) {
	d, ok := s.Src.(registry.Deleter)
	if !ok {
		return false, noDelete(registry.ErrDeleteDisabled, false)
	}
	err := d.CanDelete(r.Context(), repo)
	if err == nil {
		return true, ""
	}
	if !errors.Is(err, registry.ErrDeleteDisabled) && !errors.Is(err, registry.ErrUnauthorized) && !errors.Is(err, registry.ErrForbidden) {
		slog.WarnContext(r.Context(), "check whether deletes are allowed", "repo", repo, "err", err)
	}
	_, signedIn := s.signIn(r)
	return false, noDelete(err, signedIn)
}
