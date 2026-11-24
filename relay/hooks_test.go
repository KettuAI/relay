package relay

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// waitFor polls cond until it holds or a second passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// expectPong checks that the next message on c is the answer to a ping, so nothing came before it.
func expectPong(t *testing.T, c *websocket.Conn) {
	t.Helper()
	send(t, c, websocket.MessageText, []byte(Ping))
	if _, got, err := read(t, c); err != nil || string(got) != Pong {
		t.Fatalf("got %q %v before the pong", got, err)
	}
}

func TestHostsKeepsProfiles(t *testing.T) {
	h := newHarness(t, Options{})
	all := func(Grant) bool { return true }
	if got := h.relay.Hosts(all); len(got) != 0 {
		t.Fatalf("hosts before a sign-in: %v", got)
	}
	host := h.ready()
	got := h.relay.Hosts(all)
	if len(got) != 1 || got[0].ID != h.id || got[0].Profile != nil || got[0].Grant != keyGrant("secret") {
		t.Fatalf("hosts %+v", got)
	}
	if got := h.relay.Hosts(func(Grant) bool { return false }); len(got) != 0 {
		t.Fatalf("hosts matching nothing: %v", got)
	}
	key := Encode(make([]byte, 32))
	sendJSON(t, host, map[string]any{"type": "profile", "profile": map[string]string{"key": key, "name": "Studio", "platform": "darwin"}})
	waitFor(t, "the profile", func() bool {
		got := h.relay.Hosts(all)
		return len(got) == 1 && got[0].Profile != nil && *got[0].Profile == Profile{Key: key, Name: "Studio", Platform: "darwin"}
	})
	sendJSON(t, host, map[string]any{"type": "profile", "profile": nil})
	waitFor(t, "the profile cleared", func() bool {
		got := h.relay.Hosts(all)
		return len(got) == 1 && got[0].Profile == nil
	})
	host.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "the host gone", func() bool { return len(h.relay.Hosts(all)) == 0 })
}

// tokenGrants gives each device its connect's token as its grant, refusing "refused".
type tokenGrants struct{ keyAdmission }

func (a *tokenGrants) DeviceGrant(_ context.Context, _ string, connect DeviceMessage, _ Grant) (Grant, error) {
	if connect.Credential == "refused" {
		return nil, ErrForbidden
	}
	return keyGrant(connect.Credential), nil
}

func TestDeviceGrants(t *testing.T) {
	h := newHarness(t, Options{Admission: &tokenGrants{keyAdmission{key: "secret"}}})
	host := h.ready()
	device, accept := h.join(host, map[string]any{"type": "connect", "token": "device-a"})
	other, otherAccept := h.join(host, map[string]any{"type": "connect", "token": "device-b"})

	refused := h.dial("/v1/hosts/" + h.id + "/connect")
	sendJSON(t, refused, map[string]any{"type": "connect", "token": "refused"})
	expectClose(t, refused, CloseForbidden)

	h.relay.Revoke(func(g Grant) bool { return g == keyGrant("device-a") })
	expectClose(t, device, CloseForbidden)
	expectClose(t, accept, CloseNormal)
	// The host and the other device are not the revoked grant's.
	expectPong(t, host)
	send(t, other, websocket.MessageBinary, []byte{1})
	if _, got, err := read(t, otherAccept); err != nil || len(got) != 1 {
		t.Fatalf("the other pair: %v %v", got, err)
	}
}

func TestUndrainAndCloseAll(t *testing.T) {
	h := newHarness(t, Options{})
	h.relay.Drain()
	first := h.ready()
	if m := readText(t, first); m["type"] != "drain" {
		t.Fatalf("a host signing in while draining got %v", m)
	}
	h.relay.Undrain()
	second := h.ready()
	if m := readText(t, first); m["type"] != "replaced" {
		t.Fatalf("the first host got %v", m)
	}
	expectPong(t, second)

	h.relay.CloseAll()
	expectClose(t, second, CloseGoingAway)
	// It still takes sockets.
	h.join(h.ready(), map[string]any{"type": "connect"})
}

func TestStats(t *testing.T) {
	h := newHarness(t, Options{})
	if st := h.relay.Stats(); st != (Stats{}) {
		t.Fatalf("stats of an empty relay: %+v", st)
	}
	host := h.ready()
	device, accept := h.join(host, map[string]any{"type": "connect"})
	pending := h.dial("/v1/hosts/" + h.id + "/connect")
	before := h.relay.Stats().Bytes
	send(t, device, websocket.MessageBinary, make([]byte, 1000))
	if _, _, err := read(t, accept); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the counts", func() bool {
		st := h.relay.Stats()
		return st.Hosts == 1 && st.Devices == 1 && st.Pairs == 1 && st.Bytes >= before+2000
	})
	pending.CloseNow()
	device.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "the pair gone", func() bool {
		st := h.relay.Stats()
		return st.Hosts == 1 && st.Devices == 0 && st.Pairs == 0
	})
}
