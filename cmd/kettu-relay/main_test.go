package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/KettuAI/relay/internal/accesskey"
	"github.com/KettuAI/relay/internal/config"
	"github.com/KettuAI/relay/internal/web"
	"github.com/KettuAI/relay/relay"
)

type mockReleaseServer struct {
	mu      sync.Mutex
	tag     string
	archive []byte
	sums    string
	hits    atomic.Int32
	server  *httptest.Server
}

func newMockReleaseServer(t *testing.T) *mockReleaseServer {
	t.Helper()
	m := &mockReleaseServer{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.hits.Add(1)
		m.mu.Lock()
		defer m.mu.Unlock()
		switch r.URL.Path {
		case "/release":
			rel := map[string]any{
				"tag_name": m.tag,
				"assets": []map[string]string{
					{"name": web.Archive, "browser_download_url": m.server.URL + "/download/" + web.Archive},
					{"name": web.Checksums, "browser_download_url": m.server.URL + "/download/" + web.Checksums},
				},
			}
			_ = json.NewEncoder(w).Encode(rel)
		case "/download/" + web.Archive:
			_, _ = w.Write(m.archive)
		case "/download/" + web.Checksums:
			_, _ = io.WriteString(w, m.sums)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockReleaseServer) publish(t *testing.T, tag string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("<!doctype html><title>Kettu " + tag + "</title>")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "index.html",
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	m.mu.Lock()
	m.tag = tag
	m.archive = archive
	m.sums = hex.EncodeToString(sum[:]) + "  " + web.Archive + "\n"
	m.mu.Unlock()
}

func (m *mockReleaseServer) updater(dir string) *web.Updater {
	host := strings.TrimPrefix(m.server.URL, "http://")
	return &web.Updater{
		Dir:           dir,
		Release:       m.server.URL + "/release",
		AllowedPrefix: m.server.URL + "/download/",
		Hosts:         []string{host},
	}
}

func TestRunWebUpdateDisabledModesDoNotDownload(t *testing.T) {
	m := newMockReleaseServer(t)
	m.publish(t, "v0.1.32")

	t.Run("KETTU_RELAY_WEB=off", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg, err := config.Parse(func(k string) string {
			switch k {
			case "KETTU_RELAY_DATA":
				return dataDir
			case "KETTU_RELAY_WEB":
				return "off"
			default:
				return ""
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runWebUpdate(context.Background(), cfg, m.updater(filepath.Join(dataDir, "web")), &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out.String(), "KETTU_RELAY_WEB=off") {
			t.Fatalf("unexpected output: %q", out.String())
		}
		if m.hits.Load() != 0 {
			t.Fatalf("expected zero downloads, got %d requests", m.hits.Load())
		}
	})

	t.Run("IP-only mode", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg, err := config.Parse(func(k string) string {
			switch k {
			case "KETTU_RELAY_DATA":
				return dataDir
			case "KETTU_RELAY_IP":
				return "203.0.113.7"
			default:
				return ""
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runWebUpdate(context.Background(), cfg, m.updater(filepath.Join(dataDir, "web")), &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out.String(), "IP address") {
			t.Fatalf("unexpected output: %q", out.String())
		}
		if m.hits.Load() != 0 {
			t.Fatalf("expected zero downloads, got %d requests", m.hits.Load())
		}
	})
}

func TestRunWebUpdateHotReloadKeepsConnectionsAlive(t *testing.T) {
	m := newMockReleaseServer(t)
	m.publish(t, "v0.1.30")

	dataDir := t.TempDir()
	webDir := filepath.Join(dataDir, "web")
	cfg, err := config.Parse(func(k string) string {
		if k == "KETTU_RELAY_DATA" {
			return dataDir
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}

	store, _, err := accesskey.Load(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	updater := m.updater(webDir)

	r, err := relay.New(relay.Options{
		Admission: accesskey.Admission{Store: store},
		Origin:    func(req *http.Request) string { return "http://" + req.Host },
		Address:   func(*http.Request) string { return "127.0.0.1" },
		Features: func() []string {
			features := []string{relay.FeatureAccessKey, relay.FeatureDrain}
			if dir, _ := updater.Current(); dir != "" {
				features = append(features, relay.FeatureWeb)
			}
			return features
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pages := web.Handler(updater, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.Handles(req.URL.Path) {
			r.ServeHTTP(w, req)
			return
		}
		pages.ServeHTTP(w, req)
	}))
	defer srv.Close()

	runCtx, stopRelay := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		updater.Run(runCtx, 24*time.Hour, nil)
	}()
	defer func() {
		stopRelay()
		<-runDone
	}()

	// Wait for the running relay to pull initial v0.1.30.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, tag := updater.Current(); tag == "v0.1.30" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, tag := updater.Current(); tag != "v0.1.30" {
		t.Fatalf("relay did not load initial copy: %q", tag)
	}

	// Connect a real host and device over WebSockets and exchange a frame.
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	dial := func(p string) *websocket.Conn {
		c, _, err := websocket.Dial(context.Background(), wsBase+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	readMsg := func(c *websocket.Conn) (websocket.MessageType, []byte) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		typ, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return typ, b
	}
	readJSON := func(c *websocket.Conn) map[string]string {
		t.Helper()
		_, b := readMsg(c)
		var out map[string]string
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id := relay.HostID(key.Public().(ed25519.PublicKey))
	host := dial("/v1/hosts/" + id)
	nonce := readJSON(host)["nonce"]
	signIn, _ := json.Marshal(map[string]string{
		"type":      "sign-in",
		"publicKey": relay.Encode(key.Public().(ed25519.PublicKey)),
		"signature": relay.Encode(ed25519.Sign(key, relay.SignInText(srv.URL, id, nonce))),
		"token":     store.Key(),
	})
	if err := host.Write(context.Background(), websocket.MessageText, signIn); err != nil {
		t.Fatal(err)
	}
	if msg := readJSON(host); msg["type"] != "ready" {
		t.Fatalf("host not ready: %v", msg)
	}
	device := dial("/v1/hosts/" + id + "/connect")
	if err := device.Write(context.Background(), websocket.MessageText, []byte(`{"type":"connect","features":["drain"]}`)); err != nil {
		t.Fatal(err)
	}
	token := readJSON(host)["token"]
	accept := dial("/v1/hosts/" + id + "/accept/" + token)
	if msg := readJSON(device); msg["type"] != "joined" {
		t.Fatalf("device not joined: %v", msg)
	}
	if err := device.Write(context.Background(), websocket.MessageBinary, []byte("before-update")); err != nil {
		t.Fatal(err)
	}
	if _, b := readMsg(accept); string(b) != "before-update" {
		t.Fatalf("unexpected payload before update: %q", b)
	}

	// Publish v0.1.32 and run CLI `web update` using a fresh Updater instance.
	m.publish(t, "v0.1.32")
	var out bytes.Buffer
	if err := runWebUpdate(context.Background(), cfg, m.updater(webDir), &out); err != nil {
		t.Fatalf("runWebUpdate v0.1.32: %v", err)
	}
	if got := out.String(); got != "Updated web client from v0.1.30 to v0.1.32.\n" {
		t.Fatalf("unexpected CLI output: %q", got)
	}

	// HTTP serves v0.1.32 immediately.
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Kettu v0.1.32") {
		t.Fatalf("web page after update: %d %q", resp.StatusCode, body)
	}

	// Active host/device WebSocket connection is untouched and keeps forwarding frames.
	if err := device.Write(context.Background(), websocket.MessageBinary, []byte("after-update")); err != nil {
		t.Fatalf("device write after hot reload failed: %v", err)
	}
	if _, b := readMsg(accept); string(b) != "after-update" {
		t.Fatalf("unexpected payload after update: %q", b)
	}

	// Calling `web update` again reports already up to date.
	out.Reset()
	if err := runWebUpdate(context.Background(), cfg, m.updater(webDir), &out); err != nil {
		t.Fatalf("runWebUpdate up-to-date: %v", err)
	}
	if got := out.String(); got != "Web client is already up to date (v0.1.32).\n" {
		t.Fatalf("unexpected up-to-date CLI output: %q", got)
	}
}

func TestRunWebUpdateOfflineAndChecksumError(t *testing.T) {
	m := newMockReleaseServer(t)
	m.publish(t, "v0.1.30")

	dataDir := t.TempDir()
	webDir := filepath.Join(dataDir, "web")
	cfg, err := config.Parse(func(k string) string {
		if k == "KETTU_RELAY_DATA" {
			return dataDir
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runWebUpdate(context.Background(), cfg, m.updater(webDir), &out); err != nil {
		t.Fatalf("offline runWebUpdate: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Updated web client to v0.1.30.") || !strings.Contains(got, "No running relay answered") {
		t.Fatalf("unexpected offline output: %q", got)
	}

	// A relay started afterwards immediately serves v0.1.30 from disk.
	later := &web.Updater{Dir: webDir}
	later.Load()
	if dir, tag := later.Current(); dir == "" || tag != "v0.1.30" {
		t.Fatalf("loaded after offline update: dir=%q tag=%q", dir, tag)
	}

	// Checksum failure returns a non-nil error.
	m.publish(t, "v0.1.32")
	m.mu.Lock()
	m.sums = strings.Repeat("0", 64) + "  " + web.Archive + "\n"
	m.mu.Unlock()
	out.Reset()
	if err := runWebUpdate(context.Background(), cfg, m.updater(webDir), &out); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum error, got %v", err)
	}
}
