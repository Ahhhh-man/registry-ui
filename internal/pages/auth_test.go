package pages

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
	"github.com/Ahhhh-man/registry-ui/internal/session"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// authUI is the whole UI at base over the registry at registryURL, wired as
// main wires it: the API source behind the cache, sign-in, and error pages
// with the header.
type authUI struct {
	*Server
	h    http.Handler
	base string
}

func newAuthUI(t *testing.T, base, registryURL string) *authUI {
	t.Helper()
	api, err := registry.NewAPI(registryURL, false)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, base)
	s.Src, s.BasePath, s.Check, s.Challenged = registry.NewCache(api), base, api.Check, api.Challenged
	if s.Sessions, err = session.New("test secret", base); err != nil {
		t.Fatal(err)
	}
	return &authUI{s, web.Middleware(web.New(base, s.Handlers()), ui.ErrorRenderer(s.Templates, s.Header)), base}
}

// get serves target, as user when user isn't "" and as htmx when htmx is.
func (u *authUI) get(t *testing.T, target, user, pass string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if user != "" {
		r.AddCookie(&http.Cookie{Name: session.CookieName, Value: u.Sessions.Seal(user, pass, time.Now().Add(time.Hour))})
	}
	if htmx {
		r.Header.Set("HX-Request", "true")
		r.Header.Set("HX-Current-URL", "http://example.com"+u.base+"library/nginx?q=1")
	}
	rec := httptest.NewRecorder()
	u.h.ServeHTTP(rec, r)
	return rec
}

var authUIMarkers = []string{"Sign in", "-/login", "-/logout", "Signed in as"}

// A registry that never asks for credentials gets no sign-in or user UI.
func TestOpenRegistryHasNoAuthUI(t *testing.T) {
	u := newAuthUI(t, "/", registrytest.New(t).URL)
	for _, target := range []string{"/", "/library/nginx", "/library/nginx:1.0.0", "/nope/nothing"} {
		rec := u.get(t, target, "", "", false)
		if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d", target, rec.Code)
		}
		for _, m := range authUIMarkers {
			if strings.Contains(rec.Body.String(), m) {
				t.Errorf("%s: shows %q", target, m)
			}
		}
	}
}

func TestBasicAuthRegistry(t *testing.T) {
	u := newAuthUI(t, "/registry/", registrytest.NewWithBasicAuth(t, "alice", "secret").URL)

	// Anonymous: off to sign in, then back to this page.
	for _, target := range []string{"/registry/", "/registry/library/nginx", "/registry/library/nginx:1.0.0?x=1"} {
		rec := u.get(t, target, "", "", false)
		if want := "/registry/-/login?next=" + url.QueryEscape(target); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d to %q, want 303 to %q", target, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	// htmx can't follow a redirect into the page; HX-Redirect sends the
	// browser to sign in, then back to the page it was on.
	rec := u.get(t, "/registry/library/nginx?fragment=tags", "", "", true)
	if want := "/registry/-/login?next=" + url.QueryEscape("/registry/library/nginx?q=1"); rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != want || rec.Header().Get("Location") != "" {
		t.Errorf("htmx: %d, HX-Redirect %q, want 401 and %q", rec.Code, rec.Header().Get("HX-Redirect"), want)
	}

	// Signing in works, and pages then read the registry as alice.
	form := url.Values{"username": {"alice"}, "password": {"secret"}, "next": {"/registry/library/nginx"}}
	rec = httptest.NewRecorder()
	u.h.ServeHTTP(rec, postForm("/registry/-/login", form))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/registry/library/nginx" {
		t.Fatalf("login: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, target := range []string{"/registry/", "/registry/library/nginx", "/registry/library/nginx:1.0.0"} {
		rec = u.get(t, target, "alice", "secret", false)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "Signed in as alice") || strings.Contains(body, ">Sign in<") {
			t.Errorf("signed in %s: status %d, body:\n%s", target, rec.Code, body)
		}
	}
	if body := u.get(t, "/registry/", "alice", "secret", false).Body.String(); !strings.Contains(body, `href="/registry/library/nginx"`) {
		t.Errorf("signed-in catalog lacks %s", registrytest.RepoIndex)
	}

	// What alice saw is cached for alice only: anonymous requests still
	// have to sign in.
	for _, target := range []string{"/registry/", "/registry/library/nginx"} {
		if rec = u.get(t, target, "", "", false); rec.Code != http.StatusSeeOther || strings.Contains(rec.Body.String(), "data-row") || strings.Contains(rec.Body.String(), registrytest.TagVersion) {
			t.Errorf("anonymous %s after alice: status %d, body:\n%s", target, rec.Code, rec.Body)
		}
	}

	// The registry has challenged anonymous requests, so every page offers
	// sign-in, coming back to it.
	rec = u.get(t, "/registry/-/nope", "", "", false)
	if want := `href="/registry/-/login?next=%2Fregistry%2F-%2Fnope"`; rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), want) || !strings.Contains(rec.Body.String(), "Sign in</a>") {
		t.Errorf("404 page: status %d, lacks %s:\n%s", rec.Code, want, rec.Body)
	}

	// Credentials the registry no longer accepts don't loop through the
	// sign-in page: the page says no, with sign-in and Log out at hand.
	rec = u.get(t, "/registry/library/nginx", "alice", "old-pass", false)
	body := rec.Body.String()
	if rec.Code != http.StatusForbidden || !strings.Contains(body, "No access") || !strings.Contains(body, `href="/registry/-/login?next=%2Fregistry%2Flibrary%2Fnginx"`) || !strings.Contains(body, "Signed in as alice") {
		t.Errorf("rejected session: status %d, body:\n%s", rec.Code, body)
	}
}

// A registry that lets anyone pull but not list repositories.
func TestCatalogForbidden(t *testing.T) {
	up, err := url.Parse(registrytest.New(t).URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(up)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/_catalog" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":[{"code":"DENIED","message":"no catalog"}]}`))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	u := newAuthUI(t, "/", srv.URL)

	for _, tt := range []struct{ user, want string }{
		{"", "doesn&#39;t let anonymous users list repositories"},
		{"alice", "doesn&#39;t let these credentials list repositories"},
	} {
		rec := u.get(t, "/", tt.user, "secret", false)
		body := rec.Body.String()
		for _, want := range []string{"You can&#39;t list repositories", tt.want, `href="/-/login?next=%2F"`, "Sign in</a>"} {
			if rec.Code != http.StatusForbidden || !strings.Contains(body, want) {
				t.Errorf("user %q: status %d, lacks %s:\n%s", tt.user, rec.Code, want, body)
			}
		}
	}
	if rec := u.get(t, "/library/nginx", "", "", false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "1.0.0") {
		t.Errorf("repo page: status %d", rec.Code)
	}
	// In a fragment, the compact state offers sign-in too.
	rec := u.get(t, "/?fragment=x", "", "", true)
	if body := rec.Body.String(); rec.Code != http.StatusForbidden || strings.Contains(body, "<html") || !strings.Contains(body, `href="/-/login?next=%2Flibrary%2Fnginx%3Fq%3D1"`) {
		t.Errorf("htmx: status %d, body:\n%s", rec.Code, body)
	}
}
