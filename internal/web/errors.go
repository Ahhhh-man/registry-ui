package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/Ahhhh-man/registry-ui/internal/registry"
)

// ErrorPage is an error state for an ErrorRenderer to draw.
type ErrorPage struct {
	Status  int    // HTTP status to answer with
	Title   string // e.g. "Can't reach the registry"
	Message string // one or two sentences under the title
	// Retry is the URL of the page to try again, or "" when retrying won't help.
	Retry string
	// SignIn is the sign-in page to offer, with next set, or "".
	SignIn string
	// Inline asks for the compact state that htmx swaps into a page,
	// instead of a whole page.
	Inline bool
}

// Auth tells Error how to answer a registry 401 or 403 on a page request.
type Auth struct {
	Login    string // the sign-in page, e.g. "/-/login"
	SignedIn bool   // the request carries the session's credentials
	Catalog  bool   // the request is for the repository list
}

type authKey struct{}

// WithAuth returns r with a attached, so Error can offer sign-in. Without
// it, registry 401s and 403s just show "No access".
func WithAuth(r *http.Request, a Auth) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authKey{}, a))
}

// ErrorRenderer writes p as the response, status included.
type ErrorRenderer func(w http.ResponseWriter, r *http.Request, p ErrorPage)

var (
	notFound    = ErrorPage{Status: http.StatusNotFound, Title: "Not found", Message: "Nothing in this registry matches this address."}
	serverError = ErrorPage{Status: http.StatusInternalServerError, Title: "Something went wrong", Message: "The page couldn't be shown. The details are in the server log."}
)

// Error answers r with the error state for err and logs it. Registry errors
// pick their page: not found (404), no access (403), registry unreachable
// (502, with a retry link); anything else is a server error (500). htmx
// requests get the compact inline state instead of a whole page.
//
// With an Auth on r, an anonymous request the registry answers 401 is sent
// to the sign-in page instead (htmx gets an HX-Redirect header), and the
// no-access page offers sign-in.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	auth, _ := r.Context().Value(authKey{}).(Auth)
	p, level := serverError, slog.LevelError
	switch {
	case errors.Is(err, registry.ErrNotFound):
		p, level = notFound, slog.LevelDebug
	case errors.Is(err, registry.ErrUnauthorized) && auth.Login != "" && !auth.SignedIn:
		signIn := auth.Login + "?next=" + url.QueryEscape(retryURL(r))
		slog.DebugContext(r.Context(), "sign-in needed", "method", r.Method, "path", r.URL.Path)
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", signIn)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, signIn, http.StatusSeeOther)
		return
	case errors.Is(err, registry.ErrUnauthorized), errors.Is(err, registry.ErrForbidden):
		p, level = ErrorPage{Status: http.StatusForbidden, Title: "No access", Message: "The registry didn't allow this request."}, slog.LevelWarn
		if auth.Catalog {
			p.Title, p.Message = "You can't list repositories", "The registry doesn't let these credentials list repositories. You can still open a repository by its address."
			if !auth.SignedIn {
				p.Message = "The registry doesn't let anonymous users list repositories. Sign in, or open a repository by its address."
			}
		}
		if auth.Login != "" {
			p.SignIn = auth.Login + "?next=" + url.QueryEscape(retryURL(r))
		}
	case errors.Is(err, registry.ErrUnavailable):
		p, level = ErrorPage{Status: http.StatusBadGateway, Title: "Can't reach the registry", Message: "The registry didn't answer. It may be starting up or down; try again in a moment."}, slog.LevelWarn
		p.Retry = retryURL(r)
	}
	attrs := []any{"method", r.Method, "path", r.URL.Path, "status", p.Status, "err", err}
	if te := (*transport.Error)(nil); errors.As(err, &te) {
		attrs = append(attrs, "upstream_status", te.StatusCode)
	}
	slog.Log(r.Context(), level, "request failed", attrs...)
	renderError(w, r, p)
}

// renderError draws p with the renderer Middleware installed.
func renderError(w http.ResponseWriter, r *http.Request, p ErrorPage) {
	p.Inline = r.Header.Get("HX-Request") == "true"
	render, _ := r.Context().Value(rendererKey{}).(ErrorRenderer)
	if render == nil {
		http.Error(w, p.Title, p.Status)
		return
	}
	render(w, r, p)
}

// retryURL is the page to reload: the one the user is on. For htmx that's
// HX-Current-URL, since r may be a fragment of it; only a local path and
// query are kept, so the link never leaves the UI.
func retryURL(r *http.Request) string {
	if r.Header.Get("HX-Request") == "true" {
		u, err := url.Parse(r.Header.Get("HX-Current-URL"))
		if err == nil && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") {
			return u.RequestURI()
		}
	}
	return r.URL.RequestURI()
}
