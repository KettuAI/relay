package web

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type entry struct {
	name     string
	body     string
	typeflag byte
	link     string
}

func tarball(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: typeflag, Linkname: e.link}
		if typeflag != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

var client = []entry{
	{name: "./", typeflag: tar.TypeDir},
	{name: "./index.html", body: "<!doctype html><title>Kettu</title>"},
	{name: "./assets/", typeflag: tar.TypeDir},
	{name: "./assets/app-1234.js", body: "console.log(1)"},
}

// github is a stand-in for the release API and its downloads.
type github struct {
	mu      sync.Mutex
	tag     string
	archive []byte
	sums    string
	assets  bool
	server  *httptest.Server
}

func newGitHub(t *testing.T) *github {
	g := &github{assets: true}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch r.URL.Path {
		case "/release":
			rel := map[string]any{"tag_name": g.tag, "assets": []any{}}
			if g.assets {
				rel["assets"] = []map[string]string{
					{"name": Archive, "browser_download_url": g.server.URL + "/download/" + Archive},
					{"name": Checksums, "browser_download_url": g.server.URL + "/download/" + Checksums},
					{"name": "other.zip", "browser_download_url": g.server.URL + "/download/other.zip"},
				}
			}
			json.NewEncoder(w).Encode(rel)
		case "/download/" + Archive:
			w.Write(g.archive)
		case "/download/" + Checksums:
			io.WriteString(w, g.sums)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.server.Close)
	return g
}

func (g *github) publish(tag string, archive []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	sum := sha256.Sum256(archive)
	g.tag, g.archive = tag, archive
	g.sums = "0000000000000000000000000000000000000000000000000000000000000000  kettu-web.tar.gz\n" +
		hex.EncodeToString(sum[:]) + "  " + Archive + "\n"
}

func (g *github) updater(t *testing.T) *Updater {
	host := strings.TrimPrefix(g.server.URL, "http://")
	return &Updater{Dir: filepath.Join(t.TempDir(), "web"), Release: g.server.URL + "/release", AllowedPrefix: g.server.URL + "/download/", Hosts: []string{host}}
}

func TestUpdateAndServe(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.2.0", tarball(t, client))
	u := g.updater(t)
	ok, err := u.Update(context.Background())
	if err != nil || !ok {
		t.Fatalf("update: %v %v", ok, err)
	}
	if ok, err := u.Update(context.Background()); err != nil || ok {
		t.Fatalf("the same release is fetched again: %v %v", ok, err)
	}
	server := httptest.NewServer(Handler(u, true))
	defer server.Close()

	for path, want := range map[string]struct {
		status int
		body   string
		cache  string
	}{
		"/":                     {200, "<!doctype html><title>Kettu</title>", "no-cache"},
		"/pair":                 {200, "<!doctype html><title>Kettu</title>", "no-cache"},
		"/assets/app-1234.js":   {200, "console.log(1)", immutable},
		"/assets/missing.js":    {404, "", ""},
		"/.release":             {200, "<!doctype html><title>Kettu</title>", "no-cache"},
		"/../../etc/passwd":     {200, "<!doctype html><title>Kettu</title>", "no-cache"},
		"/assets/../index.html": {200, "<!doctype html><title>Kettu</title>", "no-cache"},
	} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want.status || (want.body != "" && string(body) != want.body) {
			t.Errorf("%s: %d %q", path, resp.StatusCode, body)
			continue
		}
		if resp.Header.Get("Strict-Transport-Security") == "" {
			t.Errorf("%s: no HSTS", path)
		}
		if want.status != 200 {
			continue
		}
		if got := resp.Header.Get("Cache-Control"); got != want.cache {
			t.Errorf("%s: Cache-Control %q", path, got)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self';") {
			t.Errorf("%s: CSP %q", path, csp)
		}
	}

	// A later start serves the kept copy before any download.
	later := &Updater{Dir: u.Dir}
	later.Load()
	if dir, tag := later.Current(); dir == "" || tag != "v0.2.0" {
		t.Fatalf("kept copy %q %q", dir, tag)
	}
}

func TestServesSandboxFramesAndViewersWithIsolatedHeaders(t *testing.T) {
	files := append([]entry{}, client...)
	files = append(files,
		entry{name: "_kettu/frame.html", body: "<!doctype html><script>1</script>"},
		entry{name: "_kettu/frame-strict.html", body: "<!doctype html><script>2</script>"},
		entry{name: "_kettu/frame-viewer.html", body: "<!doctype html><script>3</script>"},
		entry{name: "_kettu/viewer/document.js", body: "export const doc = 1"},
		entry{name: "_kettu/viewer/chunks/chunk-abc.js", body: "export const c = 2"},
	)
	g := newGitHub(t)
	g.publish("v0.2.0", tarball(t, files))
	u := g.updater(t)
	if ok, err := u.Update(context.Background()); err != nil || !ok {
		t.Fatalf("update: %v %v", ok, err)
	}
	server := httptest.NewServer(Handler(u, true))
	defer server.Close()

	for _, tc := range []struct {
		path     string
		wantCDN  bool
		wantSelf bool
		wantBody string
	}{
		{"/_kettu/frame", true, false, "<!doctype html><script>1</script>"},
		{"/_kettu/frame-strict", false, false, "<!doctype html><script>2</script>"},
		{"/_kettu/frame-viewer", false, true, "<!doctype html><script>3</script>"},
	} {
		resp, err := http.Get(server.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != tc.wantBody {
			t.Fatalf("%s: %d %q", tc.path, resp.StatusCode, body)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") {
			t.Errorf("%s: bad sandbox CSP %q", tc.path, csp)
		}
		if !strings.Contains(csp, "frame-ancestors 'self'") {
			t.Errorf("%s: missing frame-ancestors %q", tc.path, csp)
		}
		if hasCDN := strings.Contains(csp, "cdn.jsdelivr.net"); hasCDN != tc.wantCDN {
			t.Errorf("%s: CDN=%v want %v in %q", tc.path, hasCDN, tc.wantCDN, csp)
		}
		if hasSelf := strings.Contains(csp, "script-src 'self'"); hasSelf != tc.wantSelf {
			t.Errorf("%s: script-src self=%v want %v in %q", tc.path, hasSelf, tc.wantSelf, csp)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "" {
			t.Errorf("%s: unexpected X-Frame-Options %q", tc.path, got)
		}
		if got := resp.Header.Get("Permissions-Policy"); !strings.Contains(got, "camera=()") {
			t.Errorf("%s: missing Permissions-Policy %q", tc.path, got)
		}
	}

	for _, tc := range []struct {
		path   string
		status int
		cache  string
	}{
		{"/_kettu/viewer/document.js", 200, "no-cache"},
		{"/_kettu/viewer/chunks/chunk-abc.js", 200, immutable},
		{"/_kettu/viewer/missing.js", 404, ""},
	} {
		resp, err := http.Get(server.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("%s: status %d want %d", tc.path, resp.StatusCode, tc.status)
		}
		if tc.status != 200 {
			continue
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "null" {
			t.Errorf("%s: ACAO %q", tc.path, got)
		}
		if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
			t.Errorf("%s: CORP %q", tc.path, got)
		}
		if got := resp.Header.Get("Cache-Control"); got != tc.cache {
			t.Errorf("%s: Cache-Control %q want %q", tc.path, got, tc.cache)
		}
	}
}

func TestFailedUpdatesKeepTheLastGoodCopy(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.2.0", tarball(t, client))
	u := g.updater(t)
	if _, err := u.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	good, _ := u.Current()

	g.publish("v0.3.0", tarball(t, client))
	g.mu.Lock()
	g.archive = append([]byte{}, g.archive...)
	g.archive[len(g.archive)-1] ^= 1
	g.mu.Unlock()
	if _, err := u.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a changed archive: %v", err)
	}
	if dir, tag := u.Current(); dir != good || tag != "v0.2.0" {
		t.Fatal("a failed update dropped the good copy")
	}

	g.publish("v0.3.0", tarball(t, append([]entry{}, append(client, entry{name: "../escape.txt", body: "x"})...)))
	if _, err := u.Update(context.Background()); err == nil {
		t.Fatal("an archive that leaves its folder is taken")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(u.Dir), "escape.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a file landed outside the folder")
	}
	if dir, _ := u.Current(); dir != good {
		t.Fatal("a refused archive replaced the good copy")
	}

	g.mu.Lock()
	g.assets = false
	g.mu.Unlock()
	if _, err := u.Update(context.Background()); !errors.Is(err, ErrNoClient) {
		t.Fatalf("a release without the client: %v", err)
	}
}

func TestNoClientYet(t *testing.T) {
	g := newGitHub(t)
	g.mu.Lock()
	g.tag, g.assets = "v0.1.19", false
	g.mu.Unlock()
	u := g.updater(t)
	if _, err := u.Update(context.Background()); !errors.Is(err, ErrNoClient) {
		t.Fatalf("got %v", err)
	}
	server := httptest.NewServer(Handler(u, false))
	defer server.Close()
	resp, err := http.Get(server.URL + "/pair")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 || resp.Header.Get("Strict-Transport-Security") != "" {
		t.Fatalf("no client: %d", resp.StatusCode)
	}
}

func TestUnpackRefuses(t *testing.T) {
	for name, entries := range map[string][]entry{
		"parent":    {{name: "../x", body: "x"}},
		"deep":      {{name: "a/../../x", body: "x"}},
		"absolute":  {{name: "/etc/x", body: "x"}},
		"symlink":   {{name: "link", typeflag: tar.TypeSymlink, link: "/etc/passwd"}},
		"hardlink":  {{name: "link", typeflag: tar.TypeLink, link: "index.html"}},
		"device":    {{name: "dev", typeflag: tar.TypeChar}},
		"backslash": {{name: "..\\x", body: "x"}},
		"twice":     {{name: "a", body: "1"}, {name: "a", body: "2"}},
	} {
		dir := t.TempDir()
		archive := filepath.Join(t.TempDir(), "a.tar.gz")
		os.WriteFile(archive, tarball(t, entries), 0o600)
		if err := Unpack(archive, filepath.Join(dir, "out")); err == nil {
			t.Errorf("%s: taken", name)
		}
		os.MkdirAll(filepath.Join(dir, "out"), 0o700)
		if err := Unpack(archive, filepath.Join(dir, "out")); err == nil {
			t.Errorf("%s: taken", name)
		}
		if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
			t.Errorf("%s: wrote outside", name)
		}
	}
}

func TestWrappedArchive(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.2.0", tarball(t, []entry{
		{name: "kettu-web/", typeflag: tar.TypeDir},
		{name: "kettu-web/index.html", body: "page"},
	}))
	u := g.updater(t)
	if _, err := u.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir, _ := u.Current()
	if b, err := os.ReadFile(filepath.Join(dir, "index.html")); err != nil || string(b) != "page" {
		t.Fatal("the wrapped folder is not served")
	}
	// A restart while GitHub cannot be reached still serves the wrapped copy.
	g.server.Close()
	later := &Updater{Dir: u.Dir, Release: u.Release, AllowedPrefix: u.AllowedPrefix, Hosts: u.Hosts}
	later.Load()
	if got, tag := later.Current(); got != dir || tag != "v0.2.0" {
		t.Fatalf("after a restart: %q %q, want %q", got, tag, dir)
	}
	if _, err := later.Update(context.Background()); err == nil {
		t.Fatal("an update with GitHub down succeeds")
	}
	if got, _ := later.Current(); got != dir {
		t.Fatal("a failed update after a restart dropped the copy")
	}
}

func TestLoadRefusesAStoredPathOutside(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("x"), 0o600)
	web := filepath.Join(dir, "web")
	os.MkdirAll(web, 0o700)
	for _, stored := range []string{"../", "/etc", "client-x/../..", "other"} {
		os.WriteFile(filepath.Join(web, currentFile), []byte(stored+"\n"), 0o600)
		u := &Updater{Dir: web}
		u.Load()
		if got, _ := u.Current(); got != "" {
			t.Errorf("%q loads %q", stored, got)
		}
	}
}

func TestRedirectsStayOnTheReleaseHosts(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "{}")
	}))
	defer elsewhere.Close()
	g := newGitHub(t)
	redirect := httptest.NewServer(http.RedirectHandler(elsewhere.URL+"/release", http.StatusFound))
	defer redirect.Close()
	u := g.updater(t)
	u.Release = redirect.URL + "/release"
	u.Hosts = append(u.Hosts, strings.TrimPrefix(redirect.URL, "http://"))
	if _, err := u.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("a redirect off the release hosts: %v", err)
	}
	if (&Updater{}).allowed(mustURL(t, "http://github.com/x")) {
		t.Fatal("plain http to GitHub is allowed")
	}
	if !(&Updater{}).allowed(mustURL(t, "https://objects.githubusercontent.com/x")) {
		t.Fatal("GitHub's asset host is refused")
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUnpackBoundsTheWholeStream(t *testing.T) {
	before := maxStream
	maxStream = 4096
	defer func() { maxStream = before }()
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	os.WriteFile(archive, tarball(t, []entry{{name: "big", body: strings.Repeat("x", 64<<10)}}), 0o600)
	if err := Unpack(archive, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("an archive past the stream bound is unpacked")
	}
}

func TestAssetsOnlyFromTheRelease(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.2.0", tarball(t, client))
	u := g.updater(t)
	u.AllowedPrefix = "https://github.com/KettuAI/Kettu/releases/download/"
	if _, err := u.Update(context.Background()); err == nil {
		t.Fatal("assets elsewhere are fetched")
	}
}

// mirror is a stand-in for the download service: /latest redirects to the latest tag, and a
// release's files are under /download/<tag>/.
func newMirror(t *testing.T, tag string, archive []byte) (*httptest.Server, *[]string) {
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + Archive + "\n"
	var mu sync.Mutex
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/latest":
			http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
		case "/download/" + tag + "/" + Archive:
			w.Write(archive)
		case "/download/" + tag + "/" + Checksums:
			io.WriteString(w, sums)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &asked
}

func TestFallsBackToTheDownloadServiceWhenGitHubFails(t *testing.T) {
	g := newGitHub(t)
	g.server.Close()
	mirror, asked := newMirror(t, "v0.3.0", tarball(t, client))
	u := g.updater(t)
	u.Mirror = mirror.URL
	u.Hosts = append(u.Hosts, strings.TrimPrefix(mirror.URL, "http://"))
	ok, err := u.Update(context.Background())
	if err != nil || !ok {
		t.Fatalf("update from the download service: %v %v", ok, err)
	}
	if _, tag := u.Current(); tag != "v0.3.0" {
		t.Fatalf("serving %q", tag)
	}
	if ok, err := u.Update(context.Background()); err != nil || ok {
		t.Fatalf("the same release is fetched again: %v %v", ok, err)
	}
	if len(*asked) != 4 {
		t.Fatalf("asked the download service %v", *asked)
	}
}

func TestDownloadServiceOnlyWhenGitHubCannotAnswer(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.2.0", tarball(t, client))
	mirror, asked := newMirror(t, "v0.3.0", tarball(t, client))
	u := g.updater(t)
	u.Mirror = mirror.URL
	u.Hosts = append(u.Hosts, strings.TrimPrefix(mirror.URL, "http://"))
	if ok, err := u.Update(context.Background()); err != nil || !ok {
		t.Fatalf("update: %v %v", ok, err)
	}
	// GitHub answering that its latest release has no client is not a failure to reach it.
	g.mu.Lock()
	g.tag, g.assets = "v0.2.1", false
	g.mu.Unlock()
	if _, err := u.Update(context.Background()); !errors.Is(err, ErrNoClient) {
		t.Fatalf("got %v", err)
	}
	if len(*asked) != 0 {
		t.Fatalf("asked the download service %v", *asked)
	}
}

func TestDownloadServiceFilesAreChecked(t *testing.T) {
	g := newGitHub(t)
	g.server.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			http.Redirect(w, r, "/tag/v0.3.0", http.StatusFound)
		case "/download/v0.3.0/" + Archive:
			w.Write(tarball(t, client))
		case "/download/v0.3.0/" + Checksums:
			io.WriteString(w, strings.Repeat("0", 64)+"  "+Archive+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer mirror.Close()
	u := g.updater(t)
	u.Mirror = mirror.URL
	u.Hosts = append(u.Hosts, strings.TrimPrefix(mirror.URL, "http://"))
	_, err := u.Update(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum") || !strings.Contains(err.Error(), "download service") {
		t.Fatalf("a client that does not match: %v", err)
	}
	if dir, _ := u.Current(); dir != "" {
		t.Fatal("a client that does not match is served")
	}
	if !(&Updater{}).allowed(mustURL(t, "https://download.kettu.sh/latest")) {
		t.Fatal("the download service is refused")
	}
}
