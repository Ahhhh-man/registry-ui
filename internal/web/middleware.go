package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// gzipMin is the smallest HTML body worth compressing.
const gzipMin = 1024

// securityHeaders go on every response. The UI loads nothing inline and
// nothing from elsewhere, so the CSP can stay this strict.
var securityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "same-origin",
}

type rendererKey struct{}

// Middleware wraps the UI's root handler. It sets the security headers on
// every response, gzips HTML for browsers that accept it, logs one line per
// request, turns a panic into the server error page, and makes render
// available to Error. A nil render answers
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
			if p != http.ErrAbortHandler { //nolint:errorlint // as above
				rec.finish(r)
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

// statusRecorder remembers the status code a handler sent, and holds an
// HTML response back until the handler is done so finish can gzip it. Pages
// are rendered whole before they're written, so nothing is delayed.
// Anything else, static files included, goes straight through.
type statusRecorder struct {
	http.ResponseWriter
	status int
	sent   bool         // the status has gone to the real writer
	html   bytes.Buffer // the HTML body so far
}

// WriteHeader only notes the code: it is sent with the first byte of the
// body, or by finish, once the headers are final.
func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	h := w.Header()
	if !w.sent && strings.HasPrefix(h.Get("Content-Type"), "text/html") && h.Get("Content-Encoding") == "" {
		return w.html.Write(b)
	}
	w.send()
	return w.ResponseWriter.Write(b)
}

func (w *statusRecorder) send() {
	if !w.sent && w.status != 0 {
		w.sent = true
		w.ResponseWriter.WriteHeader(w.status)
	}
}

// finish sends what the handler left: the status of a response without a
// body, or the HTML held back, gzipped if r accepts it and it's big enough
// to pay. A HEAD request has no body to compress.
func (w *statusRecorder) finish(r *http.Request) {
	body := w.html.Bytes()
	if len(body) > 0 {
		h := w.Header()
		h.Add("Vary", "Accept-Encoding")
		if len(body) >= gzipMin && r.Method != http.MethodHead && AcceptsGzip(strings.Join(r.Header.Values("Accept-Encoding"), ",")) {
			var b bytes.Buffer
			zw := gzip.NewWriter(&b)
			_, _ = zw.Write(body) // a bytes.Buffer never fails
			_ = zw.Close()
			h.Set("Content-Encoding", "gzip")
			body = b.Bytes()
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.send()
	if len(body) > 0 {
		_, _ = w.ResponseWriter.Write(body)
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// AcceptsGzip reports whether an Accept-Encoding value allows gzip,
// honouring "gzip;q=0" as a refusal.
func AcceptsGzip(header string) bool {
	for part := range strings.SplitSeq(header, ",") {
		coding, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		q, ok := strings.CutPrefix(strings.TrimSpace(params), "q=")
		if !ok {
			return true
		}
		f, err := strconv.ParseFloat(q, 64)
		return err == nil && f > 0
	}
	return false
}
