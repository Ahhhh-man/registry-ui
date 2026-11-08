package web

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// securityHeaders go on every response. The UI loads nothing inline and
// nothing from elsewhere, so the CSP can stay this strict.
var securityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "same-origin",
}

type rendererKey struct{}

// Middleware wraps the UI's root handler. It sets the security headers on
// every response, logs one line per request, turns a panic into the server
// error page, and makes render available to Error. A nil render answers
// errors in plain text.
func Middleware(next http.Handler, render ErrorRenderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		for k, v := range securityHeaders {
			w.Header().Set(k, v)
		}
		r = r.WithContext(context.WithValue(r.Context(), rendererKey{}, render))
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			p := recover()
			if p != nil && p != http.ErrAbortHandler { //nolint:errorlint // recover() values are compared, not wrapped
				slog.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", p, "stack", string(debug.Stack()))
				if rec.status == 0 {
					renderError(rec, r, serverError)
				}
			}
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", status, "duration", time.Since(start))
			if p == http.ErrAbortHandler { //nolint:errorlint // as above
				panic(p)
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

// statusRecorder remembers the status code a handler sent.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
