package ui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/Ahhhh-man/registry-ui/internal/web"
)

// Set by hand: mime.TypeByExtension reads system files and doesn't know woff2.
var contentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".woff2": "font/woff2",
	".svg":   "image/svg+xml",
	".txt":   "text/plain; charset=utf-8",
}

type asset struct {
	data, gz []byte // gz is nil when gzip doesn't make the file smaller
	ctype    string
}

type assets struct {
	byHashed map[string]asset  // "app.3f9a1c2b.css"
	hashed   map[string]string // "app.css" -> "app.3f9a1c2b.css"
}

// StaticHandler serves static files by their hashed names, as returned by
// StaticName. Mount it with the URL prefix stripped. Unknown or stale names
// are a 404.
func StaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := currentAssets()
		if err != nil {
			http.Error(w, "static assets unavailable", http.StatusInternalServerError)
			return
		}
		a.ServeHTTP(w, r)
	})
}

// StaticName returns the content-hashed name of a file in static/, for example
// "app.css" -> "app.3f9a1c2b.css". An unknown name is a bug in a template, so
// it panics; template execution reports that as an error.
func StaticName(name string) string {
	a, err := currentAssets()
	if err != nil {
		panic(err)
	}
	h, ok := a.hashed[name]
	if !ok {
		panic("ui: no static file " + name)
	}
	return h
}

// Release builds hash and compress the embedded files once; dev builds
// re-read the disk on every call so edits show on refresh.
var embeddedAssets = sync.OnceValues(func() (*assets, error) { return loadAssets(staticFS(), true) })

func currentAssets() (*assets, error) {
	if dev {
		return loadAssets(staticFS(), false)
	}
	return embeddedAssets()
}

func staticFS() fs.FS {
	sub, _ := fs.Sub(files, "static") // only fails on an invalid path
	return sub
}

func loadAssets(fsys fs.FS, compress bool) (*assets, error) {
	a := &assets{byHashed: map[string]asset{}, hashed: map[string]string{}}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		ext := path.Ext(name)
		hashed := strings.TrimSuffix(name, ext) + "." + hex.EncodeToString(sum[:4]) + ext
		ctype, ok := contentTypes[ext]
		if !ok {
			ctype = "application/octet-stream"
		}
		x := asset{data: data, ctype: ctype}
		if compress {
			x.gz = gzipped(data)
		}
		a.byHashed[hashed] = x
		a.hashed[name] = hashed
		return nil
	})
	return a, err
}

func gzipped(data []byte) []byte {
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression) // only fails on a bad level
	_, _ = w.Write(data)                                  // a bytes.Buffer never fails
	_ = w.Close()
	if b.Len() >= len(data) {
		return nil
	}
	return b.Bytes()
}

func (a *assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	x, ok := a.byHashed[strings.TrimPrefix(r.URL.Path, "/")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", x.ctype)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	body := x.data
	if x.gz != nil {
		h.Set("Vary", "Accept-Encoding")
		if web.AcceptsGzip(strings.Join(r.Header.Values("Accept-Encoding"), ",")) {
			h.Set("Content-Encoding", "gzip")
			body = x.gz
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}
