package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
	"github.com/Ahhhh-man/registry-ui/internal/session"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// newDeleteUI is the whole UI over the registry at registryURL, wired as
// main wires it, with deleting on or off.
func newDeleteUI(t *testing.T, registryURL string, enabled bool) *authUI {
	t.Helper()
	api, err := registry.NewAPI(registryURL, false)
	if err != nil {
		t.Fatal(err)
	}
	return newDeleteUIOver(t, registry.NewCache(api), api.Check, enabled)
}

func newDeleteUIOver(t *testing.T, src registry.Source, check func(context.Context) error, enabled bool) *authUI {
	t.Helper()
	s := newTestServer(t, "/")
	s.Src, s.Check, s.Delete = src, check, enabled
	var err error
	if s.Sessions, err = session.New("test secret", "/"); err != nil {
		t.Fatal(err)
	}
	return &authUI{s, web.Middleware(web.New("/", s.Handlers()), ui.ErrorRenderer(s.Templates, s.Header)), "/"}
}

// post is a same-origin form POST to /-/delete, as user when user isn't "".
func (u *authUI) post(t *testing.T, form url.Values, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	r := postForm("/-/delete", form)
	if user != "" {
		r.AddCookie(&http.Cookie{Name: session.CookieName, Value: u.Sessions.Seal(user, pass, time.Now().Add(time.Hour))})
	}
	rec := httptest.NewRecorder()
	u.h.ServeHTTP(rec, r)
	return rec
}

// confirm posts back the form of the dialog in body, confirmed.
func (u *authUI) confirm(t *testing.T, body, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"confirm": {"1"}}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="(\w+)" value="([^"]*)">`).FindAllStringSubmatch(body, -1) {
		form.Add(m[1], m[2])
	}
	return u.post(t, form, user, pass)
}

const listView = "/library/nginx?tab=tags&view=list"

// Deleting latest takes 1.0.0, on the same digest, and the signature named
// after it; the dialog says so before anything goes, and afterwards nobody
// is shown them.
func TestDeleteSharedDigest(t *testing.T) {
	reg := registrytest.New(t)
	u := newDeleteUI(t, reg.URL, true)

	list := u.get(t, listView, "", "", false).Body.String()
	for _, want := range []string{`data-delete="/-/delete" data-repo="library/nginx"`, `data-action="delete"`, "Delete…"} {
		if !strings.Contains(list, want) {
			t.Errorf("list view lacks %s", want)
		}
	}

	rec := u.post(t, url.Values{"repo": {registrytest.RepoIndex}, "tag": {registrytest.TagLatest}}, "", "")
	dialog := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, dialog)
	}
	for _, want := range []string{
		`<dialog aria-labelledby="delete-title"`, "Delete 1 tag?", "This can't be undone.",
		"2 more tags will also go.", ">1.0.0</li>", `title="` + reg.SignatureTag + `"`,
		"garbage collection", `name="confirm" value="1"`, "Delete 3 tags</button>", "autofocus",
	} {
		if !strings.Contains(dialog, want) {
			t.Errorf("dialog lacks %s", want)
		}
	}
	checkMarkup(t, dialog)
	if tags, _ := u.Src.Tags(t.Context(), registrytest.RepoIndex); len(tags) != 3 {
		t.Fatalf("the preview deleted something: %v", tags)
	}

	result := u.confirm(t, dialog, "", "").Body.String()
	if !strings.Contains(result, "Deleted 3 tags") || strings.Count(result, "data-deleted=") != 3 {
		t.Errorf("result:\n%s", result)
	}
	if tags, err := u.Src.Tags(t.Context(), registrytest.RepoIndex); err != nil || len(tags) != 0 {
		t.Errorf("tags left = %v, %v", tags, err)
	}
	if list := u.get(t, listView, "", "", false).Body.String(); !strings.Contains(list, "No tags") {
		t.Errorf("the list still shows deleted tags:\n%s", list)
	}
}

// A registry that refuses deletes gets no Delete…, but a reason instead.
func TestDeleteDisabledRegistry(t *testing.T) {
	u := newDeleteUI(t, registrytest.NewDeleteDisabled(t).URL, true)
	list := u.get(t, listView, "", "", false).Body.String()
	if strings.Contains(list, `data-action="delete"`) || !strings.Contains(list, "It needs REGISTRY_STORAGE_DELETE_ENABLED=true, and pull-through caches never allow them.") {
		t.Errorf("list view:\n%s", list)
	}
	dialog := u.post(t, url.Values{"repo": {registrytest.RepoIndex}, "tag": {registrytest.TagLatest}}, "", "").Body.String()
	if !strings.Contains(dialog, "doesn&#39;t allow deletes") || strings.Contains(dialog, `name="confirm"`) {
		t.Errorf("dialog:\n%s", dialog)
	}
}

// Without the flag there's nothing to see, and no route.
func TestDeleteOff(t *testing.T) {
	u := newDeleteUI(t, registrytest.New(t).URL, false)
	list := u.get(t, listView, "", "", false).Body.String()
	for _, not := range []string{"data-delete", "Delete…", "REGISTRY_STORAGE_DELETE_ENABLED", "delete tags"} {
		if strings.Contains(list, not) {
			t.Errorf("list view shows %s", not)
		}
	}
	if rec := u.post(t, url.Values{"repo": {registrytest.RepoIndex}, "tag": {registrytest.TagLatest}}, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("POST /-/delete: status %d, want 404", rec.Code)
	}
}

// Storage mode only reads: main never turns deleting on, and even if it
// were, the storage source can't delete.
func TestDeleteStorageMode(t *testing.T) {
	r := registrytest.NewStorage(t)
	st, err := registry.NewStorage(r.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(st).(registry.Deleter); ok {
		t.Fatal("Storage can delete")
	}
	u := newDeleteUIOver(t, registry.NewCache(st), nil, true)
	if list := u.get(t, listView, "", "", false).Body.String(); strings.Contains(list, `data-action="delete"`) {
		t.Errorf("storage mode offers Delete…")
	}
	dialog := u.post(t, url.Values{"repo": {registrytest.RepoIndex}, "tag": {registrytest.TagLatest}}, "", "").Body.String()
	if strings.Contains(dialog, `name="confirm"`) {
		t.Errorf("storage mode offers to delete:\n%s", dialog)
	}
	if tags, _ := st.Tags(t.Context(), registrytest.RepoIndex); len(tags) != 3 {
		t.Errorf("tags = %v", tags)
	}
}

// One digest the registry won't delete is reported; the rest still go.
func TestDeletePartialFailure(t *testing.T) {
	reg := registrytest.New(t)
	dated, err := reg.Dated.Digest()
	if err != nil {
		t.Fatal(err)
	}
	reg.RefuseDelete(registrytest.RepoUndated, dated.String(), http.StatusForbidden)
	u := newDeleteUI(t, reg.URL, true)

	dialog := u.post(t, url.Values{"repo": {registrytest.RepoUndated}, "tag": {registrytest.TagDated, registrytest.TagLabelled}}, "", "").Body.String()
	result := u.confirm(t, dialog, "", "").Body.String()
	for _, want := range []string{"Some tags couldn't be deleted", "1 tag went from", "Not deleted: your account isn&#39;t allowed to.", `title="dated"`, `data-deleted="labelled"`} {
		if !strings.Contains(result, want) {
			t.Errorf("result lacks %s:\n%s", want, result)
		}
	}
	if strings.Contains(result, `data-deleted="dated"`) {
		t.Error("dated is reported deleted")
	}
	checkMarkup(t, result)
}

// A tag pushed onto a selected digest after the dialog opened would go
// unseen, so confirming deletes nothing and shows the dialog again.
func TestDeleteChangedSincePreview(t *testing.T) {
	reg := registrytest.New(t)
	u := newDeleteUI(t, reg.URL, true)
	dialog := u.post(t, url.Values{"repo": {registrytest.RepoSingle}, "tag": {registrytest.TagSingle}}, "", "").Body.String()

	ref, err := name.NewTag(reg.Host+"/"+registrytest.RepoSingle+":copy", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, reg.Single); err != nil {
		t.Fatal(err)
	}
	again := u.confirm(t, dialog, "", "").Body.String()
	if !strings.Contains(again, "nothing was deleted") || !strings.Contains(again, "1 more tag will also go.") || !strings.Contains(again, ">copy</li>") {
		t.Errorf("dialog:\n%s", again)
	}
	if tags, _ := u.Src.Tags(t.Context(), registrytest.RepoSingle); len(tags) != 2 {
		t.Errorf("tags = %v, want both kept", tags)
	}
}

// Only a same-origin POST with valid names reaches the registry.
func TestDeleteRequests(t *testing.T) {
	u := newDeleteUI(t, registrytest.New(t).URL, true)
	form := url.Values{"repo": {registrytest.RepoIndex}, "tag": {registrytest.TagLatest}}

	r := httptest.NewRequest(http.MethodPost, "/-/delete", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	u.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin: status %d, want 403", rec.Code)
	}

	rec = u.get(t, "/-/delete", "", "", false)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET: status %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}

	for _, bad := range []url.Values{
		{"repo": {"../etc"}, "tag": {"latest"}},
		{"repo": {registrytest.RepoIndex}, "tag": {"bad tag"}},
		{"repo": {registrytest.RepoIndex}},
	} {
		if rec := u.post(t, bad, "", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", bad, rec.Code)
		}
	}
	if tags, _ := u.Src.Tags(t.Context(), registrytest.RepoIndex); len(tags) != 3 {
		t.Errorf("tags = %v", tags)
	}
}

// Deletes sign in as the session's user; anonymous visitors are told to.
func TestDeleteSignedIn(t *testing.T) {
	u := newDeleteUI(t, registrytest.NewWithBasicAuth(t, "alice", "secret").URL, true)

	if rec := u.get(t, listView, "alice", "secret", false); !strings.Contains(rec.Body.String(), `data-action="delete"`) {
		t.Errorf("signed in: no Delete…")
	}
	dialog := u.post(t, url.Values{"repo": {registrytest.RepoSingle}, "tag": {registrytest.TagSingle}}, "alice", "secret").Body.String()
	if result := u.confirm(t, dialog, "alice", "secret").Body.String(); !strings.Contains(result, "Deleted 1 tag") {
		t.Errorf("result:\n%s", result)
	}

	if dialog := u.post(t, url.Values{"repo": {registrytest.RepoIndex}, "tag": {registrytest.TagLatest}}, "", "").Body.String(); !strings.Contains(dialog, "Sign in with an account that can delete tags") {
		t.Errorf("anonymous dialog:\n%s", dialog)
	}
}
