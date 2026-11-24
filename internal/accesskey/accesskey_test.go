package accesskey

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/KettuAI/relay/relay"
)

func TestKeyFormat(t *testing.T) {
	key := New()
	if !Valid(key) || len(key) != 4+43 || !strings.HasPrefix(key, "krk_") {
		t.Fatalf("key %q", key)
	}
	for _, bad := range []string{"", "krk_", key[4:], "krk_" + key[5:], key + "A", strings.Replace(key, "krk_", "KRK_", 1)} {
		if Valid(bad) {
			t.Errorf("%q is taken as a key", bad)
		}
	}
}

func TestLoadMakesAPrivateFileOnce(t *testing.T) {
	dir := t.TempDir()
	store, created, err := Load(dir, "")
	if err != nil || !created {
		t.Fatalf("created %v, %v", created, err)
	}
	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", info.Mode().Perm())
	}
	again, created, err := Load(dir, "")
	if err != nil || created || again.Key() != store.Key() {
		t.Fatalf("second start made another key: %v %v", created, err)
	}
	fromEnv, _, err := Load(t.TempDir(), store.Key())
	if err != nil || fromEnv.Key() != store.Key() {
		t.Fatal("the environment's key is not taken")
	}
	if _, err := fromEnv.Reset(); !errors.Is(err, ErrFromEnv) {
		t.Fatalf("reset of the environment's key: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(dir, FileName), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(dir, ""); err == nil {
			t.Fatal("a key file others can read is taken")
		}
	}
	if _, _, err := Load(t.TempDir(), "not-a-key"); err == nil || strings.Contains(err.Error(), "not-a-key") {
		t.Fatalf("a bad key in the environment: %v", err)
	}
}

func TestAdmission(t *testing.T) {
	store, _, _ := Load(t.TempDir(), "")
	a := Admission{Store: store}
	grant, err := a.Host(context.Background(), "id", store.Key())
	if err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []string{"", "krk_", store.Key() + "x", store.Key()[:len(store.Key())-1], New()} {
		if _, err := a.Host(context.Background(), "id", wrong); !errors.Is(err, relay.ErrUnauthorized) {
			t.Errorf("%q: %v", wrong, err)
		}
	}
	if err := a.Device(context.Background(), "id", relay.DeviceMessage{}, grant); err != nil {
		t.Fatal("a device without a token is refused")
	}
	if a.Stale(grant) {
		t.Fatal("a fresh grant is stale")
	}
	other, _, _ := Load(store.path[:len(store.path)-len(FileName)], "")
	if _, err := other.Reset(); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.Reload(); err != nil || !changed {
		t.Fatalf("reload after reset: %v %v", changed, err)
	}
	if !a.Stale(grant) {
		t.Fatal("a grant of the old key is not stale")
	}
	if _, err := a.Host(context.Background(), "id", other.Key()); err != nil {
		t.Fatal("the new key is refused")
	}
}

func read(t *testing.T, c *websocket.Conn) (websocket.MessageType, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Read(ctx)
}

func text(t *testing.T, c *websocket.Conn) map[string]string {
	t.Helper()
	_, b, err := read(t, c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	json.Unmarshal(b, &m)
	return m
}

func write(t *testing.T, c *websocket.Conn, typ websocket.MessageType, b []byte) {
	t.Helper()
	if err := c.Write(context.Background(), typ, b); err != nil {
		t.Fatal(err)
	}
}

// TestRelayWithAccessKey runs the relay with this admission: a host signs in with the key, a
// device with no token field joins it, they exchange bytes, and a key reset cuts the host off.
func TestRelayWithAccessKey(t *testing.T) {
	dir := t.TempDir()
	store, _, _ := Load(dir, "")
	admission := Admission{Store: store}
	r, err := relay.New(relay.Options{
		Admission: admission,
		Origin:    func(req *http.Request) string { return "http://" + req.Host },
		Address:   func(*http.Request) string { return "test" },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(r)
	defer server.Close()
	ws := "ws" + strings.TrimPrefix(server.URL, "http")
	dial := func(path string) *websocket.Conn {
		c, _, err := websocket.Dial(context.Background(), ws+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id := relay.HostID(key.Public().(ed25519.PublicKey))
	signIn := func(credential string) *websocket.Conn {
		host := dial("/v1/hosts/" + id)
		nonce := text(t, host)["nonce"]
		b, _ := json.Marshal(map[string]string{
			"type":      "sign-in",
			"publicKey": relay.Encode(key.Public().(ed25519.PublicKey)),
			"signature": relay.Encode(ed25519.Sign(key, relay.SignInText(server.URL, id, nonce))),
			"token":     credential,
		})
		write(t, host, websocket.MessageText, b)
		return host
	}

	refused := signIn(New())
	if _, _, err := read(t, refused); websocket.CloseStatus(err) != relay.CloseUnauthorized {
		t.Fatalf("a wrong key: %v", err)
	}
	host := signIn(store.Key())
	if m := text(t, host); m["type"] != "ready" {
		t.Fatalf("not ready: %v", m)
	}
	device := dial("/v1/hosts/" + id + "/connect")
	write(t, device, websocket.MessageText, []byte(`{"type":"connect","features":["drain"]}`))
	token := text(t, host)["token"]
	accept := dial("/v1/hosts/" + id + "/accept/" + token)
	if m := text(t, device); m["type"] != "joined" {
		t.Fatalf("not joined: %v", m)
	}
	write(t, device, websocket.MessageBinary, []byte("noise"))
	if _, b, err := read(t, accept); err != nil || string(b) != "noise" {
		t.Fatalf("bytes did not pass: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go store.Watch(ctx, 20*time.Millisecond, func() { r.Revoke(admission.Stale) })
	other, _, _ := Load(dir, "")
	if _, err := other.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := read(t, host); websocket.CloseStatus(err) != relay.CloseUnauthorized {
		t.Fatalf("host after the key reset: %v", err)
	}
	if _, _, err := read(t, device); websocket.CloseStatus(err) != relay.CloseForbidden {
		t.Fatalf("device after the key reset: %v", err)
	}
}
