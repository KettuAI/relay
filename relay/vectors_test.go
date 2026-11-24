package relay

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

type vectors struct {
	Key struct {
		Seed, PublicKey, ID string
	} `json:"key"`
	SignIn struct {
		Origin, Nonce, Text, Signature string
	} `json:"signIn"`
	Paths struct {
		Info, Host, Connect, Accept string
	} `json:"paths"`
	Closes         map[string]string `json:"closes"`
	HostMessages   messageSet        `json:"hostMessages"`
	RelayMessages  messageSet        `json:"relayMessages"`
	DeviceMessages messageSet        `json:"deviceMessages"`
	Drain          struct {
		Message, Connect string
	} `json:"drain"`
}

type messageSet struct {
	Valid   []string `json:"valid"`
	Invalid []string `json:"invalid"`
}

func loadVectors(t testing.TB) vectors {
	t.Helper()
	b, err := os.ReadFile("testdata/relay-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func decode(t testing.TB, s string) []byte {
	t.Helper()
	b, ok := Decode(s)
	if !ok {
		t.Fatalf("not base64url: %q", s)
	}
	return b
}

func TestVectorKeyAndSignIn(t *testing.T) {
	v := loadVectors(t)
	key := ed25519.NewKeyFromSeed(decode(t, v.Key.Seed))
	public := key.Public().(ed25519.PublicKey)
	if Encode(public) != v.Key.PublicKey {
		t.Fatalf("public key %s, want %s", Encode(public), v.Key.PublicKey)
	}
	if got := HostID(public); got != v.Key.ID {
		t.Fatalf("id %s, want %s", got, v.Key.ID)
	}
	text := SignInText(v.SignIn.Origin, v.Key.ID, v.SignIn.Nonce)
	if string(text) != v.SignIn.Text {
		t.Fatalf("sign-in text %q, want %q", text, v.SignIn.Text)
	}
	if got := Encode(ed25519.Sign(key, text)); got != v.SignIn.Signature {
		t.Fatalf("signature %s, want %s", got, v.SignIn.Signature)
	}
	signature := decode(t, v.SignIn.Signature)
	if !VerifySignIn(public, signature, v.SignIn.Origin, v.Key.ID, v.SignIn.Nonce) {
		t.Fatal("the vector's signature does not verify")
	}
	if VerifySignIn(public, signature, "https://relay.example.com", v.Key.ID, v.SignIn.Nonce) {
		t.Fatal("a signature for another origin verifies")
	}
}

func TestVectorPaths(t *testing.T) {
	v := loadVectors(t)
	for path, want := range map[string]SocketPath{
		v.Paths.Host:    {Kind: "host", ID: v.Key.ID},
		v.Paths.Connect: {Kind: "connect", ID: v.Key.ID},
		v.Paths.Accept:  {Kind: "accept", ID: v.Key.ID, Token: v.SignIn.Nonce},
	} {
		got, ok := ParseSocketPath(path)
		if !ok || got != want {
			t.Errorf("%s: %+v, want %+v", path, got, want)
		}
	}
	for _, path := range []string{v.Paths.Info, "/v1/hosts/NOT-AN-ID", v.Paths.Host + "/x", v.Paths.Host + "/accept/", "/v2/hosts/" + v.Key.ID} {
		if _, ok := ParseSocketPath(path); ok {
			t.Errorf("%s reads as a socket path", path)
		}
	}
}

func TestVectorCloses(t *testing.T) {
	v := loadVectors(t)
	for code, reason := range loadVectors(t).Closes {
		n, _ := strconv.Atoi(code)
		if got := Refusal(n); got != reason {
			t.Errorf("%d: %q, want %q", n, got, reason)
		}
	}
	if len(v.Closes) != 8 || Refusal(CloseNormal) != "" {
		t.Fatal("close codes do not match the vectors")
	}
}

func TestVectorMessages(t *testing.T) {
	v := loadVectors(t)
	for _, text := range v.HostMessages.Valid {
		if _, ok := ParseHostMessage([]byte(text)); !ok {
			t.Errorf("host message refused: %s", text)
		}
	}
	for _, text := range v.HostMessages.Invalid {
		if _, ok := ParseHostMessage([]byte(text)); ok {
			t.Errorf("host message taken: %s", text)
		}
	}
	sent := map[string]bool{
		string(challengeMessage(v.SignIn.Nonce)): true,
		string(msgReady):                         true,
		string(incomingMessage(v.SignIn.Nonce)):  true,
		string(msgReplaced):                      true,
		string(msgDrain):                         true,
	}
	for _, text := range v.RelayMessages.Valid {
		if !sent[text] {
			t.Errorf("the relay does not send %s as the vectors write it", text)
		}
	}
	for _, text := range v.DeviceMessages.Valid {
		m, ok := ParseDeviceMessage([]byte(text))
		if !ok || !m.HasCredential || m.Credential != "access-token" {
			t.Errorf("device message refused: %s", text)
		}
	}
	for _, text := range v.DeviceMessages.Invalid {
		m, ok := ParseDeviceMessage([]byte(text))
		// A connect without a token is the one this relay takes, which an Admission decides on.
		if text == `{"type":"connect"}` {
			if !ok || m.HasCredential {
				t.Errorf("a connect without a token is refused")
			}
			continue
		}
		if ok {
			t.Errorf("device message taken: %s", text)
		}
	}
	if string(msgDrain) != v.Drain.Message {
		t.Errorf("drain is %s, want %s", msgDrain, v.Drain.Message)
	}
	m, ok := ParseDeviceMessage([]byte(v.Drain.Connect))
	if !ok || len(m.Features) != 1 || m.Features[0] != FeatureDrain {
		t.Errorf("a connect asking for drain reads as %+v", m)
	}
	if string(msgJoined) != `{"type":"joined"}` {
		t.Errorf("joined is %s", msgJoined)
	}
}
