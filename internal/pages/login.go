package pages

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// loginData is the data for the "login" template.
type loginData struct {
	Registry string // registry title, named in the explanation
	Next     string // where to go after signing in, already checked by localNext
	User     string // the username to fill in again after a failure
	Failed   bool   // the registry rejected the last attempt
}

// Login serves /-/login: the sign-in form on GET, and on POST a check of the
// credentials against the registry. Accepted credentials go into the session
// cookie and the browser is sent on to next; rejected ones get the form
// again with a 401.
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.loginForm(w, r, http.StatusOK, loginData{Next: localNext(s.BasePath, r.URL.Query().Get("next"))})
	case http.MethodPost:
		if !sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		d := loginData{Next: localNext(s.BasePath, r.PostFormValue("next")), User: r.PostFormValue("username")}
		pass := r.PostFormValue("password")
		err := registry.ErrUnauthorized
		if d.User != "" && pass != "" {
			err = s.Check(registry.WithCredentials(r.Context(), d.User, pass))
		}
		switch {
		case err == nil:
			s.Sessions.Set(w, r, d.User, pass)
			http.Redirect(w, r, d.Next, http.StatusSeeOther)
		case errors.Is(err, registry.ErrUnauthorized), errors.Is(err, registry.ErrForbidden):
			slog.Info("sign-in rejected", "user", d.User)
			d.Failed = true
			s.loginForm(w, r, http.StatusUnauthorized, d)
		default:
			web.Error(w, r, err) // the registry couldn't answer: its error page, with a retry
		}
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Logout serves /-/logout. Only POST signs out, so a link or an image on
// another page can't; it clears the session and goes back to the catalog.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	s.Sessions.Clear(w, r)
	http.Redirect(w, r, s.BasePath, http.StatusSeeOther)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request, status int, d loginData) {
	d.Registry = s.Title
	if status != http.StatusOK {
		// page sets the type as it writes, which is too late once the status is out.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
	}
	s.page(w, r, ui.Page{PageTitle: "Sign in", SignIn: true}, "login", d)
}

// localNext returns next when it is a plain path under base, and base
// otherwise. Browsers treat "//host", "/\host" and paths with tabs or
// newlines (which they strip) as other sites, so those are refused, as are
// dot segments that climb out of base.
func localNext(base, next string) string {
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.ContainsFunc(next, func(c rune) bool { return c == '\\' || c < 0x20 || c == 0x7f }) ||
		!strings.HasPrefix(path.Clean(u.Path)+"/", base) {
		return base
	}
	return next
}

// sameOrigin reports whether a POST came from a page of this site: its
// Origin, or Referer when there is no Origin, must name the host the request
// was sent to. SameSite=Strict already keeps the cookie off cross-site
// requests; this also stops another site signing a visitor in or out.
// X-Forwarded-Host counts for proxies that rewrite Host; a cross-site form
// can't set that header.
func sameOrigin(r *http.Request) bool {
	from := r.Header.Get("Origin")
	if from == "" {
		from = r.Header.Get("Referer")
	}
	u, err := url.Parse(from)
	if err != nil || u.Host == "" {
		return false
	}
	fwd, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")
	return strings.EqualFold(u.Host, r.Host) || strings.EqualFold(u.Host, strings.TrimSpace(fwd))
}
