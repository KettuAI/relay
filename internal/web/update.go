// Package web keeps and serves the Kettu web client built for relays, so a browser reaches a
// computer through this relay without any hosted page. The client comes from the latest Kettu
// release on GitHub and is used only when its SHA-256 matches the release's checksum file.
package web

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Asset names in a Kettu release.
const (
	Archive   = "kettu-web-relay.tar.gz"
	Checksums = "kettu-web-sha256sums.txt"
)

const (
	// DefaultRelease is the latest release of the Kettu code repository.
	DefaultRelease = "https://api.github.com/repos/KettuAI/Kettu/releases/latest"
	// downloadPrefix is where the release's assets must be.
	downloadPrefix = "https://github.com/KettuAI/Kettu/releases/download/"

	maxArchive   = 256 << 20
	maxUnpacked  = 512 << 20
	maxFiles     = 10000
	maxChecksums = 1 << 20
	maxRelease   = 4 << 20

	currentFile = "current"
)

// ErrNoClient means the latest release has no web client for relays.
var ErrNoClient = errors.New("the latest Kettu release has no web client for relays")

// Updater downloads the web client into Dir and knows which copy to serve.
type Updater struct {
	// Dir holds the unpacked copies and the name of the current one.
	Dir string
	// Release is the GitHub API URL of the release to read; DefaultRelease when "".
	Release string
	// AllowedPrefix is where asset downloads may point; the Kettu releases when "".
	AllowedPrefix string
	Client        *http.Client

	mu      sync.Mutex
	current string
	tag     string
}

// Load finds the copy a previous run kept, so the relay serves it before any download.
func (u *Updater) Load() {
	name, err := os.ReadFile(filepath.Join(u.Dir, currentFile))
	if err != nil {
		return
	}
	dir := filepath.Join(u.Dir, filepath.Base(strings.TrimSpace(string(name))))
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return
	}
	tag, _ := os.ReadFile(filepath.Join(dir, ".release"))
	u.set(dir, strings.TrimSpace(string(tag)))
}

func (u *Updater) set(dir, tag string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.current, u.tag = dir, tag
}

// Current is the directory of the copy being served and its release, or "" with none.
func (u *Updater) Current() (dir, tag string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.current, u.tag
}

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Update fetches the latest release's client unless it is the one served, and switches to it
// once checked and unpacked. It reports whether it switched; on any failure the copy served
// before stays.
func (u *Updater) Update(ctx context.Context) (bool, error) {
	var rel release
	if err := u.getJSON(ctx, or(u.Release, DefaultRelease), &rel); err != nil {
		return false, err
	}
	if _, tag := u.Current(); tag != "" && tag == rel.Tag {
		return false, nil
	}
	var archiveURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case Archive:
			archiveURL = a.URL
		case Checksums:
			sumsURL = a.URL
		}
	}
	if archiveURL == "" || sumsURL == "" {
		return false, ErrNoClient
	}
	prefix := or(u.AllowedPrefix, downloadPrefix)
	if !strings.HasPrefix(archiveURL, prefix) || !strings.HasPrefix(sumsURL, prefix) {
		return false, errors.New("the release's assets are not where Kettu releases are")
	}
	sums, err := u.get(ctx, sumsURL, maxChecksums)
	if err != nil {
		return false, err
	}
	want, err := checksum(sums, Archive)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(u.Dir, 0o700); err != nil {
		return false, err
	}
	archive, err := u.download(ctx, archiveURL, want)
	if err != nil {
		return false, err
	}
	defer os.Remove(archive)

	dir, err := os.MkdirTemp(u.Dir, "client-")
	if err != nil {
		return false, err
	}
	if err := Unpack(archive, dir); err != nil {
		os.RemoveAll(dir)
		return false, err
	}
	root, err := clientRoot(dir)
	if err != nil {
		os.RemoveAll(dir)
		return false, err
	}
	if err := os.WriteFile(filepath.Join(root, ".release"), []byte(rel.Tag+"\n"), 0o600); err != nil {
		os.RemoveAll(dir)
		return false, err
	}
	rel1, _ := filepath.Rel(u.Dir, root)
	if err := writeAtomic(filepath.Join(u.Dir, currentFile), []byte(rel1+"\n")); err != nil {
		os.RemoveAll(dir)
		return false, err
	}
	before, _ := u.Current()
	u.set(root, rel.Tag)
	u.prune(dir, before)
	return true, nil
}

// prune removes copies other than the one just unpacked and the one served until now, which a
// request may still be reading.
func (u *Updater) prune(keep, before string) {
	entries, err := os.ReadDir(u.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(u.Dir, e.Name())
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "client-") || p == keep || (before != "" && strings.HasPrefix(before, p)) {
			continue
		}
		os.RemoveAll(p)
	}
}

// Run updates at once and then every interval until ctx is done, logging what changed.
func (u *Updater) Run(ctx context.Context, interval time.Duration, logf func(string, ...any)) {
	for {
		ok, err := u.Update(ctx)
		_, tag := u.Current()
		switch {
		case err != nil && tag != "":
			logf("web client: update failed (%v); still serving %s", err, tag)
		case errors.Is(err, ErrNoClient):
			logf("web client: not served, since the latest Kettu release has none for relays yet")
		case err != nil:
			logf("web client: not served, since the download failed: %v", err)
		case ok:
			logf("web client: serving %s", tag)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

var sumLine = regexp.MustCompile(`^([0-9a-fA-F]{64}) [ *](.+)$`)

func checksum(sums []byte, name string) ([]byte, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		m := sumLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m != nil && path.Base(m[2]) == name {
			return hex.DecodeString(m[1])
		}
	}
	return nil, fmt.Errorf("%s does not list %s", Checksums, name)
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (u *Updater) request(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "kettu-relay")
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	return resp, nil
}

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := u.request(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("the answer is too large")
	}
	return b, nil
}

func (u *Updater) getJSON(ctx context.Context, url string, v any) error {
	b, err := u.get(ctx, url, maxRelease)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// download writes the archive to a temporary file and keeps it only when its SHA-256 is want.
func (u *Updater) download(ctx context.Context, url string, want []byte) (string, error) {
	resp, err := u.request(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(u.Dir, ".download-*")
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, maxArchive+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
	case n > maxArchive:
		err = errors.New("the web client archive is too large")
	case !equal(hash.Sum(nil), want):
		err = errors.New("the web client archive does not match its checksum")
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func equal(a, b []byte) bool { return hex.EncodeToString(a) == hex.EncodeToString(b) }

// Unpack writes a gzipped tar of plain files and folders into dir. It refuses the whole archive
// for any entry that would land outside dir, a link or another special file, or one too large.
func Unpack(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()

	tr := tar.NewReader(gz)
	var total int64
	for files := 0; ; files++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if files >= maxFiles {
			return errors.New("the web client archive has too many files")
		}
		name, err := entryName(h.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if h.Size < 0 || total > maxUnpacked {
				return errors.New("the web client archive is too large unpacked")
			}
			if parent := path.Dir(name); parent != "." {
				if err := root.MkdirAll(parent, 0o700); err != nil {
					return err
				}
			}
			out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, h.Size))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("the web client archive has %q, which is not a plain file or folder", h.Name)
		}
	}
}

// entryName is an archive entry's path inside the folder, "" for the folder itself.
func entryName(name string) (string, error) {
	if strings.Contains(name, "\\") || strings.ContainsRune(name, 0) || path.IsAbs(name) {
		return "", fmt.Errorf("the web client archive has the path %q", name)
	}
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || !fs.ValidPath(clean) {
		return "", fmt.Errorf("the web client archive has the path %q", name)
	}
	return clean, nil
}

// clientRoot is the folder with index.html: dir itself, or the one folder the archive wrapped
// its files in.
func clientRoot(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		return dir, nil
	}
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 1 && entries[0].IsDir() {
		inner := filepath.Join(dir, entries[0].Name())
		if _, err := os.Stat(filepath.Join(inner, "index.html")); err == nil {
			return inner, nil
		}
	}
	return "", errors.New("the web client archive has no index.html")
}

func writeAtomic(name string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(name), ".current-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
