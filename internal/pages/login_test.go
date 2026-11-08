package pages

import (
	"net/http"
	"net/http/httptest"
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

// newLoginServer returns a test Server at base whose logins are checked by a
// registry that accepts only alice / secret.
func newLoginServer(t *testing.T, base string) *Server {
	t.Helper()
	s := newTestServer(t, base)
	api, err := registry.NewAPI(registrytest.NewWithBasicAuth(t, "alice", "secret").URL, false)
	if err != nil {
		t.Fatal(err)
	}
	s.BasePath, s.Check = base, api.Check
	if s.Sessions, err = session.New("test secret", base); err != nil {
		t.Fatal(err)
	}
	return s
}

// postForm is a same-origin form POST to target.
func postForm(target string, form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://example.com") // httptest's Host
	return r
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == session.CookieName {
			return c
		}
	}
	return nil
}

func TestLocalNext(t *testing.T) {
	for _, tt := range []struct{ base, next, want string }{
		{"/", "/x", "/x"},
		{"/", "/library/nginx:1.27?tab=tags", "/library/nginx:1.27?tab=tags"},
		{"/registry/", "/registry/x", "/registry/x"},
		{"/registry/", "/registry/", "/registry/"},
		{"/registry/", "/x", "/registry/"},
		{"/registry/", "/registryx", "/registry/"},
		{"/registry/", "/registry/../x", "/registry/"},
		{"/registry/", "/registry/%2e%2e/x", "/registry/"},
		{"/", "", "/"},
		{"/", "x", "/"},
		{"/", "//evil.com", "/"},
		{"/", "//evil.com/x", "/"},
		{"/", "https://evil.com", "/"},
		{"/", "https://example.com/x", "/"},
		{"/", `/\evil.com`, "/"},
		{"/", `\\evil.com`, "/"},
		{"/", "/\t/evil.com", "/"},
		{"/", "/\n/evil.com", "/"},
		{"/", "javascript:alert(1)", "/"},
		{"/", "/x%", "/"},
	} {
		if got := localNext(tt.base, tt.next); got != tt.want {
			t.Errorf("localNext(%q, %q) = %q, want %q", tt.base, tt.next, got, tt.want)
		}
	}
}

func TestLoginForm(t *testing.T) {
	s := newLoginServer(t, "/registry/")
	rec := httptest.NewRecorder()
	s.Login(rec, httptest.NewRequest(http.MethodGet, "/registry/-/login?next=/registry/library/nginx", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{
		`<form method="post" action="/registry/-/login"`,
		`name="next" value="/registry/library/nginx"`,
		`<strong class="font-semibold break-all text-text">registry.example.com</strong> requires authentication`,
		`autocomplete="current-password"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form is missing %q", want)
		}
	}
	for _, unwanted := range []string{"rejected", `role="search"`, "style=", "<script>"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("form contains %q", unwanted)
		}
	}
}

func TestLoginSuccess(t *testing.T) {
	s := newLoginServer(t, "/")
	for next, want := range map[string]string{
		"/library/nginx:1.0": "/library/nginx:1.0",
		"":                   "/",
		"//evil.com":         "/",
		"https://evil.com/":  "/",
	} {
		rec := httptest.NewRecorder()
		s.Login(rec, postForm("/-/login", url.Values{"username": {"alice"}, "password": {"secret"}, "next": {next}}))
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Errorf("next=%q: %d to %q, want 303 to %q", next, rec.Code, rec.Header().Get("Location"), want)
		}
		c := sessionCookie(rec)
		if c == nil {
			t.Fatalf("next=%q: no session cookie", next)
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(c)
		if u, p, ok := s.Sessions.Get(r); !ok || u != "alice" || p != "secret" {
			t.Errorf("session holds %q, %q, %v", u, p, ok)
		}
	}
}

func TestLoginRejected(t *testing.T) {
	s := newLoginServer(t, "/")
	for _, form := range []url.Values{
		{"username": {"alice"}, "password": {"wrong-pass"}},
		{"username": {"bob"}, "password": {"secret"}},
		{"username": {"alice"}, "password": {""}},
		{"username": {""}, "password": {"wrong-pass"}},
	} {
		form.Set("next", "/library/nginx")
		rec := httptest.NewRecorder()
		s.Login(rec, postForm("/-/login", form))
		body := rec.Body.String()
		if rec.Code != http.StatusUnauthorized || !strings.Contains(body, "The registry rejected these credentials.") {
			t.Errorf("%v: status %d, body:\n%s", form, rec.Code, body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%v: Content-Type = %q", form, ct)
		}
		if sessionCookie(rec) != nil {
			t.Errorf("%v: set a session cookie", form)
		}
		if !strings.Contains(body, `value="`+form.Get("username")+`"`) || !strings.Contains(body, `value="/library/nginx"`) {
			t.Errorf("%v: username or next not kept", form)
		}
		if strings.Contains(body, "wrong-pass") {
			t.Errorf("%v: password echoed", form)
		}
	}
}

func TestLoginRegistryDown(t *testing.T) {
	s := newLoginServer(t, "/")
	api, err := registry.NewAPI("http://127.0.0.1:1", false)
	if err != nil {
		t.Fatal(err)
	}
	s.Check = api.Check
	rec := httptest.NewRecorder()
	s.Login(rec, postForm("/-/login", url.Values{"username": {"alice"}, "password": {"secret"}}))
	if rec.Code != http.StatusBadGateway || sessionCookie(rec) != nil {
		t.Errorf("status = %d, want 502 and no cookie", rec.Code)
	}
}

func TestSameOrigin(t *testing.T) {
	for _, tt := range []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"same Origin", map[string]string{"Origin": "http://example.com"}, true},
		{"same Origin, other case", map[string]string{"Origin": "https://EXAMPLE.com"}, true},
		{"same Referer", map[string]string{"Referer": "http://example.com/-/login?next=/x"}, true},
		{"forwarded host", map[string]string{"Origin": "https://registry.example.org", "X-Forwarded-Host": "registry.example.org"}, true},
		{"none", nil, false},
		{"other Origin", map[string]string{"Origin": "https://evil.com"}, false},
		{"other port", map[string]string{"Origin": "http://example.com:8080"}, false},
		{"null Origin", map[string]string{"Origin": "null"}, false},
		{"other Origin beats same Referer", map[string]string{"Origin": "https://evil.com", "Referer": "http://example.com/"}, false},
		{"other Referer", map[string]string{"Referer": "https://example.com.evil.com/"}, false},
		{"relative Referer", map[string]string{"Referer": "/-/login"}, false},
	} {
		r := httptest.NewRequest(http.MethodPost, "/-/login", nil)
		for k, v := range tt.headers {
			r.Header.Set(k, v)
		}
		if got := sameOrigin(r); got != tt.want {
			t.Errorf("%s: sameOrigin = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCrossOriginPostsRefused(t *testing.T) {
	s := newLoginServer(t, "/")
	for path, h := range map[string]http.HandlerFunc{"/-/login": s.Login, "/-/logout": s.Logout} {
		r := postForm(path, url.Values{"username": {"alice"}, "password": {"secret"}})
		r.Header.Set("Origin", "https://evil.com")
		rec := httptest.NewRecorder()
		h(rec, r)
		if rec.Code != http.StatusForbidden || sessionCookie(rec) != nil {
			t.Errorf("%s: status %d, cookie %v; want 403 and no cookie", path, rec.Code, sessionCookie(rec))
		}
	}
}

func TestLogout(t *testing.T) {
	s := newLoginServer(t, "/registry/")
	rec := httptest.NewRecorder()
	s.Logout(rec, postForm("/registry/-/logout", nil))
	c := sessionCookie(rec)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/registry/" {
		t.Errorf("%d to %q, want 303 to /registry/", rec.Code, rec.Header().Get("Location"))
	}
	if c == nil || c.MaxAge >= 0 || c.Path != "/registry/" {
		t.Errorf("cookie = %+v, want it deleted at /registry/", c)
	}

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		rec = httptest.NewRecorder()
		s.Logout(rec, httptest.NewRequest(m, "/registry/-/logout", nil))
		if rec.Code != http.StatusMethodNotAllowed || sessionCookie(rec) != nil {
			t.Errorf("%s: status %d, want 405 and no cookie", m, rec.Code)
		}
	}
}

func TestHeaderShowsSignedInUser(t *testing.T) {
	s := newLoginServer(t, "/")
	signedIn := httptest.NewRequest(http.MethodGet, "/", nil)
	signedIn.AddCookie(&http.Cookie{Name: session.CookieName, Value: s.Sessions.Seal("alice", "secret", time.Now().Add(time.Hour))})

	for _, tt := range []struct {
		r    *http.Request
		want bool
	}{{signedIn, true}, {httptest.NewRequest(http.MethodGet, "/", nil), false}} {
		rec := httptest.NewRecorder()
		s.page(rec, tt.r, ui.Page{}, "error", web.ErrorPage{Title: "x"})
		body := rec.Body.String()
		got := strings.Contains(body, "Signed in as alice") && strings.Contains(body, `<form method="post" action="/-/logout"><button type="submit" aria-label="Log out"`)
		if got != tt.want {
			t.Errorf("signed in %v: header user shown = %v", tt.want, got)
		}
	}

	// The sign-in page itself has no search box or user menu.
	rec := httptest.NewRecorder()
	s.Login(rec, signedIn)
	if body := rec.Body.String(); strings.Contains(body, "Signed in as") || strings.Contains(body, `role="search"`) {
		t.Error("sign-in page shows the search box or user menu")
	}
}

func TestHandlersRouteLogin(t *testing.T) {
	if h := newTestServer(t, "/").Handlers(); h.Login != nil || h.Logout != nil {
		t.Error("login routed without sessions")
	}
	if h := newLoginServer(t, "/").Handlers(); h.Login == nil || h.Logout == nil {
		t.Error("login not routed")
	}
}

// A Source that can't check credentials, such as storage mode, has no
// sign-in: the routes are 404 and the header links to neither.
func TestNoSignInWithoutCheck(t *testing.T) {
	s := newLoginServer(t, "/")
	s.Check = nil
	h := web.New("/", s.Handlers())
	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/-/login", nil),
		postForm("/-/login", url.Values{"username": {"alice"}, "password": {"secret"}}),
		postForm("/-/logout", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound || sessionCookie(rec) != nil {
			t.Errorf("%s %s: status %d, want 404 and no cookie", r.Method, r.URL.Path, rec.Code)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: session.CookieName, Value: s.Sessions.Seal("alice", "secret", time.Now().Add(time.Hour))})
	rec := httptest.NewRecorder()
	s.page(rec, r, ui.Page{}, "error", web.ErrorPage{Title: "x"})
	if body := rec.Body.String(); strings.Contains(body, "Signed in as") || strings.Contains(body, "/-/logout") {
		t.Error("header offers the user menu without sign-in")
	}
}
