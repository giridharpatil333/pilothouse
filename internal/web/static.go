package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// The PWA: plain HTML, CSS and ES modules, no build step.
//
//go:embed static
var staticFS embed.FS

type staticFile struct {
	body  []byte
	ctype string
	etag  string
}

type staticFiles struct{ files map[string]staticFile }

func loadStatic() (*staticFiles, error) {
	sf := &staticFiles{files: map[string]staticFile{}}
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		ct := mime.TypeByExtension(path.Ext(p))
		switch path.Ext(p) {
		case ".webmanifest":
			ct = "application/manifest+json"
		case ".js", ".mjs":
			ct = "text/javascript; charset=utf-8"
		case ".svg":
			ct = "image/svg+xml"
		}
		if ct == "" {
			ct = http.DetectContentType(b)
		}
		sf.files["/"+strings.TrimPrefix(p, "static/")] = staticFile{body: b, ctype: ct, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		return nil
	})
	return sf, err
}

// serve writes a file with a strong ETag. Assets aren't fingerprinted, so
// they revalidate on every load (cheap on loopback/tailnet), which keeps
// the service worker and the app in lockstep after an upgrade.
func (sf *staticFiles) serve(w http.ResponseWriter, r *http.Request, name string) bool {
	f, ok := sf.files[name]
	if !ok {
		return false
	}
	h := w.Header()
	h.Set("Content-Type", f.ctype)
	h.Set("ETag", f.etag)
	h.Set("Cache-Control", "no-cache")
	if name == "/sw.js" {
		h.Set("Service-Worker-Allowed", "/")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.body))
	return true
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	s.static.serve(w, r, "/index.html")
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(r.URL.Path)
	if name == "/favicon.ico" {
		name = "/icons/favicon.png"
	}
	if name == "/index.html" || !s.static.serve(w, r, name) {
		http.NotFound(w, r)
	}
}
