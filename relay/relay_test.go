package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// keyAdmission takes hosts whose credential is key and every device; a grant names its key.
type keyAdmission struct {
	mu  sync.Mutex
	key string
}

type keyGrant string

func (keyGrant) Deadline() time.Time { return time.Time{} }

func (a *keyAdmission) Host(_ context.Context, _ string, credential string) (Grant, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if credential != a.key {
		return nil, ErrUnauthorized
	}
	return keyGrant(credential), nil
}

func (a *keyAdmission) Renew(ctx context.Context, id string, _ Grant, credential string) (Grant, error) {
	return a.Host(ctx, id, credential)
}

func (a *keyAdmission) Device(context.Context, string, DeviceMessage, Grant) error { return nil }

type harness struct {
	t      *testing.T
	relay  *Relay
	server *httptest.Server
	origin string
	key    ed25519.PrivateKey
	id     string
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{t: t}
	if opts.Admission == nil {
		opts.Admission = &keyAdmission{key: "secret"}
	}
	opts.Origin = func(r *http.Request) string { return "http://" + r.Host }
	if opts.Address == nil {
		opts.Address = func(r *http.Request) string {
			if a := r.Header.Get("X-Test-Address"); a != "" {
				return a
			}
			return "test"
		}
	}
	opts.Features = func() []string { return []string{FeatureAccessKey, FeatureDrain} }
	opts.Version = "test"
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	h.relay = r
	h.server = httptest.NewServer(r)
	t.Cleanup(h.server.Close)
	h.origin = h.server.URL
	_, h.key, _ = ed25519.GenerateKey(nil)
	h.id = HostID(h.key.Public().(ed25519.PublicKey))
	return h
}

func (h *harness) dial(path string) *websocket.Conn {
	h.t.Helper()
	c, status, err := h.dialFrom("test", path)
	if err != nil {
		h.t.Fatalf("dial %s: %d %v", path, status, err)
	}
	return c
}

// dialFrom opens a socket as if from address, returning the HTTP status of a refused upgrade.
func (h *harness) dialFrom(address, path string) (*websocket.Conn, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, strings.Replace(h.origin, "http", "ws", 1)+path, &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Test-Address": {address}},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return nil, status, err
	}
	c.SetReadLimit(1 << 20)
	h.t.Cleanup(func() { c.CloseNow() })
	return c, 0, nil
}

func read(t *testing.T, c *websocket.Conn) (websocket.MessageType, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Read(ctx)
}

func readText(t *testing.T, c *websocket.Conn) map[string]string {
	t.Helper()
	typ, data, err := read(t, c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("binary where text was due")
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not a message: %s", data)
	}
	return m
}

func send(t *testing.T, c *websocket.Conn, typ websocket.MessageType, data []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, typ, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func sendJSON(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	send(t, c, websocket.MessageText, b)
}

// expectClose reads until the socket closes and checks its code.
func expectClose(t *testing.T, c *websocket.Conn, code int) {
	t.Helper()
	for {
		_, _, err := read(t, c)
		if err == nil {
			continue
		}
		if got := int(websocket.CloseStatus(err)); got != code {
			t.Fatalf("closed with %d (%v), want %d", got, err, code)
		}
		return
	}
}

func (h *harness) signIn(credential string) *websocket.Conn {
	h.t.Helper()
	c := h.dial("/v1/hosts/" + h.id)
	challenge := readText(h.t, c)
	if challenge["type"] != "challenge" || !IsToken(challenge["nonce"]) {
		h.t.Fatalf("no challenge: %v", challenge)
	}
	signature := ed25519.Sign(h.key, SignInText(h.origin, h.id, challenge["nonce"]))
	sendJSON(h.t, c, map[string]string{
		"type":      "sign-in",
		"publicKey": Encode(h.key.Public().(ed25519.PublicKey)),
		"signature": Encode(signature),
		"token":     credential,
	})
	return c
}

func (h *harness) ready() *websocket.Conn {
	h.t.Helper()
	c := h.signIn("secret")
	if m := readText(h.t, c); m["type"] != "ready" {
		h.t.Fatalf("not ready: %v", m)
	}
	return c
}

// join connects a device with connect, has the host accept it and returns both data sockets.
func (h *harness) join(host *websocket.Conn, connect any) (device, accept *websocket.Conn) {
	h.t.Helper()
	device = h.dial("/v1/hosts/" + h.id + "/connect")
	sendJSON(h.t, device, connect)
	incoming := readText(h.t, host)
	if incoming["type"] != "incoming" || !IsToken(incoming["token"]) {
		h.t.Fatalf("no incoming: %v", incoming)
	}
	accept = h.dial("/v1/hosts/" + h.id + "/accept/" + incoming["token"])
	if m := readText(h.t, device); m["type"] != "joined" {
		h.t.Fatalf("not joined: %v", m)
	}
	return device, accept
}

func TestInfo(t *testing.T) {
	h := newHarness(t, Options{})
	resp, err := http.Get(h.origin + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info["relay"] != "kettu" || info["protocol"] != float64(1) || info["version"] != "test" {
		t.Fatalf("info %v", info)
	}
	if regions, ok := info["regions"].([]any); !ok || len(regions) != 0 {
		t.Fatalf("regions %v, want an empty array", info["regions"])
	}
	features, _ := json.Marshal(info["features"])
	if string(features) != `["access-key","drain"]` {
		t.Fatalf("features %s", features)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("GET /v1 is not readable from other origins")
	}
	for path, status := range map[string]int{"/v1/ping": 204, "/v1/hosts": 404, "/v1/hosts/" + h.id: 426} {
		resp, err := http.Get(h.origin + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != status {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, status)
		}
	}
}

func TestHostAndDeviceExchangeBinary(t *testing.T) {
	h := newHarness(t, Options{})
	host := h.ready()
	device, accept := h.join(host, map[string]any{"type": "connect", "features": []string{"drain"}})

	big := bytes.Repeat([]byte{0xa5}, MaxMessage)
	for i, message := range [][]byte{{0, 1, 2}, big, []byte("not text, bytes")} {
		send(t, device, websocket.MessageBinary, message)
		typ, got, err := read(t, accept)
		if err != nil || typ != websocket.MessageBinary || !bytes.Equal(got, message) {
			t.Fatalf("device to host %d: %v %v", i, typ, err)
		}
		reply := append([]byte("re:"), message[:3]...)
		send(t, accept, websocket.MessageBinary, reply)
		typ, got, err = read(t, device)
		if err != nil || typ != websocket.MessageBinary || !bytes.Equal(got, reply) {
			t.Fatalf("host to device %d: %v %v", i, typ, err)
		}
	}
	for _, c := range []*websocket.Conn{host, device, accept} {
		send(t, c, websocket.MessageText, []byte(Ping))
		if typ, got, err := read(t, c); err != nil || typ != websocket.MessageText || string(got) != Pong {
			t.Fatalf("ping got %q %v", got, err)
		}
	}
	// The device going closes the host's side of the pair, and the host stays signed in.
	device.Close(websocket.StatusNormalClosure, "")
	expectClose(t, accept, CloseNormal)
	h.join(host, map[string]any{"type": "connect"})
}

func TestDeviceTokens(t *testing.T) {
	h := newHarness(t, Options{})
	host := h.ready()
	for _, connect := range []any{
		map[string]any{"type": "connect"},
		map[string]any{"type": "connect", "token": ""},
		map[string]any{"type": "connect", "token": "anything", "features": []string{"drain", "unknown"}},
	} {
		h.join(host, connect)
	}
	for connect, code := range map[string]int{
		`{"type":"connect","token":7}`:          CloseForbidden,
		`{"type":"connect","features":"drain"}`: CloseForbidden,
		`{"type":"hello"}`:                      CloseMalformed,
		`ping-not`:                              CloseMalformed,
	} {
		device := h.dial("/v1/hosts/" + h.id + "/connect")
		send(t, device, websocket.MessageText, []byte(connect))
		expectClose(t, device, code)
	}
}

func TestRefusals(t *testing.T) {
	h := newHarness(t, Options{})

	device := h.dial("/v1/hosts/" + h.id + "/connect")
	sendJSON(t, device, map[string]any{"type": "connect"})
	expectClose(t, device, CloseOffline)

	wrong := h.signIn("not the key")
	expectClose(t, wrong, CloseUnauthorized)

	c := h.dial("/v1/hosts/" + h.id)
	readText(t, c)
	sendJSON(t, c, map[string]string{"type": "token", "token": "secret"})
	expectClose(t, c, CloseMalformed)

	// A signature for another id's key.
	otherKey, _, _ := ed25519.GenerateKey(nil)
	other := h.dial("/v1/hosts/" + HostID(otherKey))
	challenge := readText(t, other)
	sendJSON(t, other, map[string]string{
		"type":      "sign-in",
		"publicKey": Encode(h.key.Public().(ed25519.PublicKey)),
		"signature": Encode(ed25519.Sign(h.key, SignInText(h.origin, h.id, challenge["nonce"]))),
		"token":     "secret",
	})
	expectClose(t, other, CloseUnauthorized)

	accept := h.dial("/v1/hosts/" + h.id + "/accept/" + NewToken())
	expectClose(t, accept, CloseOffline)
	notToken := h.dial("/v1/hosts/" + h.id + "/accept/short")
	expectClose(t, notToken, CloseOffline)
}

func TestReplaced(t *testing.T) {
	h := newHarness(t, Options{})
	first := h.ready()
	device, accept := h.join(first, map[string]any{"type": "connect"})
	waiting := h.dial("/v1/hosts/" + h.id + "/connect")
	sendJSON(t, waiting, map[string]any{"type": "connect"})
	readText(t, first)

	second := h.ready()
	if m := readText(t, first); m["type"] != "replaced" {
		t.Fatalf("no replaced: %v", m)
	}
	expectClose(t, first, CloseReplaced)
	expectClose(t, waiting, CloseOffline)
	// A pair already joined is the host's data socket and stays.
	send(t, device, websocket.MessageBinary, []byte{9})
	if _, got, err := read(t, accept); err != nil || !bytes.Equal(got, []byte{9}) {
		t.Fatalf("the joined pair broke: %v", err)
	}
	h.join(second, map[string]any{"type": "connect"})
}

func TestJoinedSocketRules(t *testing.T) {
	h := newHarness(t, Options{})
	host := h.ready()

	device, accept := h.join(host, map[string]any{"type": "connect"})
	send(t, device, websocket.MessageText, []byte(`{"type":"connect"}`))
	expectClose(t, device, CloseMalformed)
	expectClose(t, accept, CloseMalformed)

	device, accept = h.join(host, map[string]any{"type": "connect"})
	send(t, accept, websocket.MessageBinary, bytes.Repeat([]byte{1}, MaxMessage+1))
	expectClose(t, accept, CloseTooBig)
	expectClose(t, device, CloseTooBig)
}

func TestLimits(t *testing.T) {
	h := newHarness(t, Options{ConnectionsPerMinute: 1000, DevicesPerMinute: 1000})
	host := h.ready()
	for range MaxDevices {
		h.join(host, map[string]any{"type": "connect"})
	}
	// Joined devices fill the host's slots; one more is refused once it asks.
	extra := h.dial("/v1/hosts/" + h.id + "/connect")
	sendJSON(t, extra, map[string]any{"type": "connect"})
	expectClose(t, extra, CloseTooMany)

	h2 := newHarness(t, Options{ConnectionsPerMinute: 2, DevicesPerMinute: 1000})
	h2.dial("/v1/hosts/" + h2.id + "/connect")
	h2.dial("/v1/hosts/" + h2.id + "/connect")
	third := h2.dial("/v1/hosts/" + h2.id + "/connect")
	expectClose(t, third, CloseTooMany)

	h3 := newHarness(t, Options{ConnectionsPerMinute: 1000, DevicesPerMinute: 1})
	h3.dial("/v1/hosts/" + h3.id + "/connect")
	again := h3.dial("/v1/hosts/" + h3.id + "/connect")
	expectClose(t, again, CloseTooMany)
}

func TestRevokeAndDrain(t *testing.T) {
	admission := &keyAdmission{key: "secret"}
	h := newHarness(t, Options{Admission: admission})
	host := h.ready()
	plain, plainAccept := h.join(host, map[string]any{"type": "connect"})
	drained, _ := h.join(host, map[string]any{"type": "connect", "features": []string{"drain"}})

	h.relay.Drain()
	if m := readText(t, host); m["type"] != "drain" {
		t.Fatalf("host got %v, want drain", m)
	}
	if m := readText(t, drained); m["type"] != "drain" {
		t.Fatalf("device got %v, want drain", m)
	}
	send(t, plain, websocket.MessageBinary, []byte{1})
	if _, got, err := read(t, plainAccept); err != nil || !bytes.Equal(got, []byte{1}) {
		t.Fatal("a device that did not ask was told to drain")
	}

	admission.mu.Lock()
	admission.key = "new"
	admission.mu.Unlock()
	h.relay.Revoke(func(g Grant) bool { return g != keyGrant("new") })
	expectClose(t, host, CloseUnauthorized)
	expectClose(t, plain, CloseForbidden)
	expectClose(t, plainAccept, CloseNormal)

	if h.relay.Sockets() != 0 {
		time.Sleep(200 * time.Millisecond)
	}
	if n := h.relay.Sockets(); n != 0 {
		t.Fatalf("%d sockets left", n)
	}
}

func TestSignInTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the sign-in timeout")
	}
	h := newHarness(t, Options{})
	c := h.dial("/v1/hosts/" + h.id)
	readText(t, c)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), SignInTimeout+5*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if code := websocket.CloseStatus(err); int(code) != CloseTimeout || time.Since(start) < SignInTimeout-time.Second {
		t.Fatalf("closed with %d after %v", code, time.Since(start))
	}
}

func TestShutdown(t *testing.T) {
	h := newHarness(t, Options{})
	host := h.ready()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { h.relay.Shutdown(ctx); close(done) }()
	if m := readText(t, host); m["type"] != "drain" {
		t.Fatalf("host got %v, want drain", m)
	}
	expectClose(t, host, CloseGoingAway)
	<-done
	if _, status, err := h.dialFrom("test", "/v1/hosts/"+h.id); err == nil || status != http.StatusServiceUnavailable {
		t.Fatalf("a socket after shutdown: %d %v", status, err)
	}
}

// live has the host's side of a pair answer, as a host that took the device does.
func live(t *testing.T, accept *websocket.Conn) {
	t.Helper()
	send(t, accept, websocket.MessageBinary, []byte{0})
}

func TestFloodOfOneHostLeavesRoomForARealDevice(t *testing.T) {
	h := newHarness(t, Options{ConnectionsPerMinute: 10000, DevicesPerMinute: 10000, JoinTimeout: 300 * time.Millisecond})
	host := h.ready()

	// Idle sockets from many addresses that never send their connect.
	for i := range 3 * DefaultMaxPendingDevices {
		h.dialFrom("idle-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "/v1/hosts/"+h.id+"/connect")
	}
	// Devices that join but that the host never takes, which cannot speak the channel.
	var silent []*websocket.Conn
	for range MaxDevices {
		device, _ := h.join(host, map[string]any{"type": "connect"})
		silent = append(silent, device)
	}
	for _, device := range silent {
		expectClose(t, device, CloseTimeout)
	}

	device, accept := h.join(host, map[string]any{"type": "connect", "features": []string{"drain"}})
	live(t, accept)
	time.Sleep(500 * time.Millisecond)
	send(t, device, websocket.MessageBinary, []byte("hello"))
	if _, got, err := read(t, accept); err != nil || string(got) != "hello" {
		t.Fatalf("the real device did not get through: %v", err)
	}
}

func TestFloodOfTheRelayLeavesRoomForARealDevice(t *testing.T) {
	h := newHarness(t, Options{ConnectionsPerMinute: 10000, DevicesPerMinute: 10000, MaxConnections: 24, MaxConnectionsPerAddress: 8})
	host := h.ready()

	// One address holds no more than its share, refused before any upgrade.
	for range 8 {
		if _, _, err := h.dialFrom("attacker", "/v1/hosts/"+h.id+"/connect"); err != nil {
			t.Fatal(err)
		}
	}
	if _, status, err := h.dialFrom("attacker", "/v1/hosts/"+h.id+"/connect"); err == nil || status != http.StatusTooManyRequests {
		t.Fatalf("past the address's share: %d %v", status, err)
	}
	device, accept := h.join(host, map[string]any{"type": "connect"})
	live(t, accept)

	// Many addresses fill the relay; past its cap requests are refused before the upgrade.
	var held []*websocket.Conn
	for i := 0; ; i++ {
		c, status, err := h.dialFrom("many-"+string(rune('a'+i)), "/v1/hosts/"+h.id+"/connect")
		if err != nil {
			if status != http.StatusServiceUnavailable {
				t.Fatalf("past the relay's cap: %d %v", status, err)
			}
			break
		}
		held = append(held, c)
	}
	// The pair joined before keeps working.
	send(t, device, websocket.MessageBinary, []byte{1})
	if _, got, err := read(t, accept); err != nil || !bytes.Equal(got, []byte{1}) {
		t.Fatal("a joined pair broke under the flood")
	}
	// Once some go, a real device gets in.
	for _, c := range held[:4] {
		c.CloseNow()
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.relay.Connections() > 24-4 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	next, nextAccept := h.join(host, map[string]any{"type": "connect"})
	live(t, nextAccept)
	send(t, next, websocket.MessageBinary, []byte{2})
	if _, got, err := read(t, nextAccept); err != nil || !bytes.Equal(got, []byte{2}) {
		t.Fatal("a real device did not get in after the flood eased")
	}
}

func TestJoinTimeoutSparesALivePair(t *testing.T) {
	h := newHarness(t, Options{JoinTimeout: 200 * time.Millisecond})
	host := h.ready()
	silent, silentAccept := h.join(host, map[string]any{"type": "connect"})
	device, accept := h.join(host, map[string]any{"type": "connect"})
	live(t, accept)
	if _, got, err := read(t, device); err != nil || !bytes.Equal(got, []byte{0}) {
		t.Fatal("the host's answer did not pass")
	}
	expectClose(t, silent, CloseTimeout)
	expectClose(t, silentAccept, CloseTimeout)
	time.Sleep(300 * time.Millisecond)
	send(t, device, websocket.MessageBinary, []byte{5})
	if _, got, err := read(t, accept); err != nil || !bytes.Equal(got, []byte{5}) {
		t.Fatal("a live pair was closed")
	}
}

// gatedAdmission holds each host check until it is let through, to order it against a rotation.
type gatedAdmission struct {
	keyAdmission
	entered chan struct{}
	release chan struct{}
}

func (a *gatedAdmission) Host(ctx context.Context, id, credential string) (Grant, error) {
	grant, err := a.keyAdmission.Host(ctx, id, credential)
	a.entered <- struct{}{}
	<-a.release
	return grant, err
}

func TestRotationDuringASignIn(t *testing.T) {
	admission := &gatedAdmission{keyAdmission: keyAdmission{key: "old"}, entered: make(chan struct{}), release: make(chan struct{})}
	h := newHarness(t, Options{Admission: admission})
	stale := func(g Grant) bool { return g == keyGrant("old") }

	// The key changes after the check took the old key and before its grant is installed.
	c := h.signIn("old")
	<-admission.entered
	admission.mu.Lock()
	admission.key = "new"
	admission.mu.Unlock()
	h.relay.Revoke(stale)
	admission.release <- struct{}{}
	expectClose(t, c, CloseUnauthorized)

	// The key changes before the check starts: the old key is refused by the check itself.
	c = h.signIn("old")
	<-admission.entered
	admission.release <- struct{}{}
	expectClose(t, c, CloseUnauthorized)

	// A sign-in with the new key is not held to the earlier rotation.
	c = h.signIn("new")
	<-admission.entered
	admission.release <- struct{}{}
	if m := readText(t, c); m["type"] != "ready" {
		t.Fatalf("the new key: %v", m)
	}
}
