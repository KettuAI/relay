package web

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// pageCSP lets the page reach nothing but its own origin, where the relay is.
const pageCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; media-src 'self' data: blob:; font-src 'self' data:; " +
	"connect-src 'self'; frame-src 'self'; worker-src 'self' blob:; object-src 'none'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

const (
	sandboxDirective = "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-forms allow-modals"
	artifactCDNs     = " https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com https://esm.sh https://cdn.tailwindcss.com https://fonts.googleapis.com https://fonts.gstatic.com"

	// Isolated HTTP CSPs for sandboxed artifact shell pages, enforcing opaque origin.
	sandboxDefaultCSP = sandboxDirective + "; frame-ancestors 'self'; default-src 'none'; " +
		"script-src 'unsafe-inline' 'unsafe-eval' blob: data:" + artifactCDNs + "; " +
		"style-src 'unsafe-inline' blob: data:" + artifactCDNs + "; " +
		"font-src data: blob:" + artifactCDNs + "; " +
		"img-src data: blob:" + artifactCDNs + "; " +
		"media-src data: blob:; " +
		"connect-src data: blob:" + artifactCDNs + "; " +
		"worker-src blob:; frame-src blob: data:; form-action 'none'; base-uri 'none'"

	sandboxStrictCSP = sandboxDirective + "; frame-ancestors 'self'; default-src 'none'; " +
		"script-src 'unsafe-inline' 'unsafe-eval' blob: data:; " +
		"style-src 'unsafe-inline' blob: data:; " +
		"font-src data: blob:; " +
		"img-src data: blob:; " +
		"media-src data: blob:; " +
		"connect-src data: blob:; " +
		"worker-src blob:; frame-src blob: data:; form-action 'none'; base-uri 'none'"

	sandboxViewerCSP = sandboxDirective + "; frame-ancestors 'self'; default-src 'none'; " +
		"script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
		"font-src 'self' data:; img-src data: blob:; media-src data: blob:; " +
		"connect-src data: blob:; form-action 'none'; base-uri 'none'"

	artifactPermissions = "camera=(), microphone=(), geolocation=(), display-capture=(), " +
		"usb=(), serial=(), hid=(), bluetooth=(), midi=(), payment=(), " +
		"clipboard-read=(), clipboard-write=(), fullscreen=()"
)

type sandboxRoute struct {
	file string
	csp  string
}

var sandboxRoutes = map[string]sandboxRoute{
	"_kettu/frame":             {file: "_kettu/frame.html", csp: sandboxDefaultCSP},
	"_kettu/frame.html":        {file: "_kettu/frame.html", csp: sandboxDefaultCSP},
	"_kettu/frame-strict":      {file: "_kettu/frame-strict.html", csp: sandboxStrictCSP},
	"_kettu/frame-strict.html": {file: "_kettu/frame-strict.html", csp: sandboxStrictCSP},
	"_kettu/frame-viewer":      {file: "_kettu/frame-viewer.html", csp: sandboxViewerCSP},
	"_kettu/frame-viewer.html": {file: "_kettu/frame-viewer.html", csp: sandboxViewerCSP},
}

const immutable = "public, max-age=31536000, immutable"

const writeDeadline = time.Minute

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
		// A reader that stalls is cut off rather than holding the connection.
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(writeDeadline))
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
		if route, ok := sandboxRoutes[name]; ok {
			if !serveSandboxFrame(w, r, root, route) {
				http.NotFound(w, r)
			}
			return
		}
		if strings.HasPrefix(name, "_kettu/viewer/") {
			if hidden || !serveViewerFile(w, r, root, name) {
				http.NotFound(w, r)
			}
			return
		}
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

// serveSandboxFrame serves a static sandbox shell with its own HTTP CSP and no X-Frame-Options: DENY
// (embedding is restricted by frame-ancestors 'self').
func serveSandboxFrame(w http.ResponseWriter, r *http.Request, root *os.Root, route sandboxRoute) bool {
	if !fs.ValidPath(route.file) {
		return false
	}
	f, err := root.Open(route.file)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	h := w.Header()
	h.Set("Content-Security-Policy", route.csp)
	h.Set("Permissions-Policy", artifactPermissions)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "", info.ModTime(), f)
	return true
}

// serveViewerFile serves built-in document and slides viewer assets for read-only CORS loads from
// opaque-origin sandbox frames.
func serveViewerFile(w http.ResponseWriter, r *http.Request, root *os.Root, name string) bool {
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
	cache := "no-cache"
	if strings.HasPrefix(name, "_kettu/viewer/chunks/") || strings.HasPrefix(name, "_kettu/viewer/assets/") {
		cache = immutable
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "null")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", cache)
	if t, ok := types[path.Ext(name)]; ok {
		h.Set("Content-Type", t)
	} else {
		h.Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, "", info.ModTime(), f)
	return true
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
