package web

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// pageCSP lets the page reach nothing but its own origin, where the relay is.
const pageCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; media-src 'self' data: blob:; font-src 'self' data:; " +
	"connect-src 'self'; frame-src 'self'; worker-src 'self' blob:; object-src 'none'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

const immutable = "public, max-age=31536000, immutable"

var types = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".webp":        "image/webp",
	".woff2":       "font/woff2",
	".json":        "application/json",
	".wasm":        "application/wasm",
	".ico":         "image/x-icon",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

// Handler serves the copy the updater holds: files under /assets/ as they are, every other path
// that is no file as the client's page, since /pair and the client's other routes are its own.
// With hsts it tells browsers to come back over https alone.
func Handler(u *Updater, hsts bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		dir, _ := u.Current()
		if dir == "" {
			http.Error(w, "This relay has no web client yet.", http.StatusNotFound)
			return
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		defer root.Close()

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		hidden := strings.HasPrefix(name, ".") || strings.Contains(name, "/.")
		if strings.HasPrefix(name, "assets/") {
			if hidden || !serveFile(w, r, root, name, immutable) {
				http.NotFound(w, r)
			}
			return
		}
		if name != "" && !hidden && serveFile(w, r, root, name, "no-cache") {
			return
		}
		if !serveFile(w, r, root, "index.html", "no-cache") {
			http.NotFound(w, r)
		}
	})
}

func serveFile(w http.ResponseWriter, r *http.Request, root *os.Root, name, cache string) bool {
	if !fs.ValidPath(name) {
		return false
	}
	f, err := root.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	h := w.Header()
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", cache)
	if t, ok := types[path.Ext(name)]; ok {
		h.Set("Content-Type", t)
	} else {
		h.Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, "", info.ModTime(), f)
	return true
}
