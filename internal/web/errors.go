package web

import (
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
	// Inline asks for the compact state that htmx swaps into a page,
	// instead of a whole page.
	Inline bool
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
func Error(w http.ResponseWriter, r *http.Request, err error) {
	p, level := serverError, slog.LevelError
	switch {
	case errors.Is(err, registry.ErrNotFound):
		p, level = notFound, slog.LevelDebug
	case errors.Is(err, registry.ErrUnauthorized), errors.Is(err, registry.ErrForbidden):
		// Seam for sign-in (#23): a 401 will redirect to the login page.
		p, level = ErrorPage{Status: http.StatusForbidden, Title: "No access", Message: "The registry didn't allow this request."}, slog.LevelWarn
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
