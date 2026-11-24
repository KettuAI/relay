// Package relay is the Kettu relay protocol 1 and a relay that serves it: hosts sign in on
// /v1/hosts/<id>, devices ask for a host on /v1/hosts/<id>/connect, and the host accepts each on
// /v1/hosts/<id>/accept/<token>. The relay then passes binary messages unchanged; they are the
// end-to-end encrypted channel's, which the relay cannot read. Who may sign in is up to an
// Admission, so builds of the relay can admit by different credentials.
package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Protocol is the relay protocol GET /v1 names.
const Protocol = 1

// SignInContext is the first line of the text a host signs.
const SignInContext = "kettu-relay/1"

// Ping and Pong are the text keepalive any socket may send and the relay's answer.
const (
	Ping = "ping"
	Pong = "pong"
)

const (
	// SignInTimeout is how long a host's control socket has from the challenge to its sign-in.
	SignInTimeout = 10 * time.Second
	// ConnectTimeout is how long a device's socket has from opening to its connect.
	ConnectTimeout = 10 * time.Second
	// AcceptTimeout is how long a host has from incoming to opening the accept socket.
	AcceptTimeout = 10 * time.Second
	// MaxMessage is the longest message in bytes the relay passes; a longer one closes with 1009.
	MaxMessage = 64 * 1024
	// MaxDevices is how many device sockets one host id may have at once.
	MaxDevices = 16
)

// Features a relay may list on GET /v1.
const (
	// FeatureDrain: the relay tells ready hosts, and joined devices that asked, to move before
	// it stops.
	FeatureDrain = "drain"
	// FeatureAccessKey: hosts sign in with the relay's access key and devices bring no token.
	FeatureAccessKey = "access-key"
	// FeatureWeb: the relay serves the web client at its own origin.
	FeatureWeb = "web"
)

// Close codes, each with the reason clients show.
const (
	CloseNormal       = 1000
	CloseGoingAway    = 1001
	CloseTooBig       = 1009
	CloseUnavailable  = 1011
	CloseMalformed    = 4400
	CloseUnauthorized = 4401
	CloseForbidden    = 4403
	CloseOffline      = 4404
	CloseTimeout      = 4408
	CloseReplaced     = 4409
	CloseTooMany      = 4429
)

// Refusal names the reason behind a relay close code, as clients do; "" for any other code.
func Refusal(code int) string {
	switch code {
	case CloseMalformed:
		return "malformed"
	case CloseUnauthorized:
		return "unauthorized"
	case CloseForbidden:
		return "forbidden"
	case CloseOffline:
		return "offline"
	case CloseTimeout:
		return "timeout"
	case CloseReplaced:
		return "replaced"
	case CloseTooBig:
		return "too_big"
	case CloseTooMany:
		return "too_many"
	}
	return ""
}

const (
	maxCredential = 4096
	maxFeatures   = 8
	maxFeature    = 32
	maxName       = 64
)

var (
	hostIDPattern   = regexp.MustCompile(`^[a-z2-7]{26}$`)
	tokenPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
	platformPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	idEncoding      = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
)

// IsHostID reports whether s is a host id: 26 characters of lowercase base32.
func IsHostID(s string) bool { return hostIDPattern.MatchString(s) }

// IsToken reports whether s is a one-time token or nonce as the relay hands them out.
func IsToken(s string) bool { return tokenPattern.MatchString(s) }

// HostID is the relay id of an Ed25519 public key: the first 16 bytes of its SHA-256 in base32.
func HostID(publicKey []byte) string {
	sum := sha256.Sum256(publicKey)
	return idEncoding.EncodeToString(sum[:16])
}

// SignInText is what a host signs: the context, the relay's origin, its id and the nonce.
func SignInText(origin, id, nonce string) []byte {
	return []byte(strings.Join([]string{SignInContext, origin, id, nonce}, "\n"))
}

// Encode is base64url without padding, the protocol's encoding for bytes.
func Encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Decode reads base64url without padding, as clients write it.
func Decode(s string) ([]byte, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return b, err == nil
}

// NewToken is 32 random bytes in base64url, for nonces and accept tokens.
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return Encode(b)
}

// VerifySignIn reports whether publicKey is the id's and signed the sign-in text.
func VerifySignIn(publicKey, signature []byte, origin, id, nonce string) bool {
	if len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return false
	}
	if HostID(publicKey) != id {
		return false
	}
	return ed25519.Verify(publicKey, SignInText(origin, id, nonce), signature)
}

// Profile is how a relay with the hosts feature lists a host. This relay has no such feature
// and drops profiles, but reads them so a host that sends one is not cut off.
type Profile struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

// HostMessage is a text message from a host on its control socket.
type HostMessage struct {
	// Type is "sign-in", "token" or "profile".
	Type      string
	PublicKey []byte
	Signature []byte
	// Credential is the token field: the access key on this relay.
	Credential string
	Profile    *Profile
}

// DeviceMessage is a device's connect.
type DeviceMessage struct {
	// Credential is the token field, "" when the device sent none, as on this relay.
	Credential string
	// HasCredential reports whether the token field was there.
	HasCredential bool
	// Features are those of the device's that the relay knows.
	Features []string
}

func object(text []byte) (map[string]json.RawMessage, bool) {
	if len(text) > MaxMessage || !utf8.Valid(text) {
		return nil, false
	}
	trimmed := bytes.TrimSpace(text)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(trimmed, &fields) != nil {
		return nil, false
	}
	return fields, true
}

func stringField(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := fields[name]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func messageType(text []byte) (string, map[string]json.RawMessage, bool) {
	fields, ok := object(text)
	if !ok {
		return "", nil, false
	}
	t, ok := stringField(fields, "type")
	return t, fields, ok
}

func isCredential(s string) bool { return len(s) > 0 && len(s) <= maxCredential }

func parseProfile(raw json.RawMessage) (*Profile, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	key, ok1 := stringField(fields, "key")
	name, ok2 := stringField(fields, "name")
	platform, ok3 := stringField(fields, "platform")
	if !ok1 || !ok2 || !ok3 {
		return nil, false
	}
	if b, ok := Decode(key); !ok || len(b) != 32 {
		return nil, false
	}
	if !isProfileName(name) || !platformPattern.MatchString(platform) {
		return nil, false
	}
	return &Profile{Key: key, Name: name, Platform: platform}, true
}

// isProfileName counts UTF-16 units, as clients measure the 64 characters.
func isProfileName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name {
		return false
	}
	units := 0
	for _, r := range name {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
		units++
		if r >= 0x10000 {
			units++
		}
	}
	return units <= maxName
}

// ParseHostMessage reads a text message from a host; false when it is none the protocol has.
func ParseHostMessage(text []byte) (HostMessage, bool) {
	t, fields, ok := messageType(text)
	if !ok {
		return HostMessage{}, false
	}
	switch t {
	case "token":
		credential, ok := stringField(fields, "token")
		if !ok || !isCredential(credential) {
			return HostMessage{}, false
		}
		return HostMessage{Type: t, Credential: credential}, true
	case "profile":
		raw, ok := fields["profile"]
		if !ok {
			return HostMessage{}, false
		}
		if string(bytes.TrimSpace(raw)) == "null" {
			return HostMessage{Type: t}, true
		}
		profile, ok := parseProfile(raw)
		if !ok {
			return HostMessage{}, false
		}
		return HostMessage{Type: t, Profile: profile}, true
	case "sign-in":
		publicKey, ok1 := stringField(fields, "publicKey")
		signature, ok2 := stringField(fields, "signature")
		credential, ok3 := stringField(fields, "token")
		if !ok1 || !ok2 || !ok3 || !isCredential(credential) {
			return HostMessage{}, false
		}
		key, ok1 := Decode(publicKey)
		sig, ok2 := Decode(signature)
		if !ok1 || !ok2 || len(key) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
			return HostMessage{}, false
		}
		return HostMessage{Type: t, PublicKey: key, Signature: sig, Credential: credential}, true
	}
	return HostMessage{}, false
}

// IsConnect reports whether a device's text is a connect at all, whatever its fields.
func IsConnect(text []byte) bool {
	t, _, ok := messageType(text)
	return ok && t == "connect"
}

// ParseDeviceMessage reads a device's connect. The token may be missing or empty, which an
// Admission decides on; a token that is not a string, or features that are not an array, are not
// a connect.
func ParseDeviceMessage(text []byte) (DeviceMessage, bool) {
	t, fields, ok := messageType(text)
	if !ok || t != "connect" {
		return DeviceMessage{}, false
	}
	var message DeviceMessage
	if raw, ok := fields["token"]; ok {
		if err := json.Unmarshal(raw, &message.Credential); err != nil || len(message.Credential) > maxCredential {
			return DeviceMessage{}, false
		}
		message.HasCredential = true
	}
	if raw, ok := fields["features"]; ok {
		var features []json.RawMessage
		if json.Unmarshal(raw, &features) != nil || features == nil {
			return DeviceMessage{}, false
		}
		if len(features) > maxFeatures {
			features = features[:maxFeatures]
		}
		for _, raw := range features {
			var feature string
			if json.Unmarshal(raw, &feature) == nil && feature == FeatureDrain {
				message.Features = []string{FeatureDrain}
			}
		}
	}
	return message, true
}

// The relay's messages, as text.
var (
	msgReady    = []byte(`{"type":"ready"}`)
	msgReplaced = []byte(`{"type":"replaced"}`)
	msgDrain    = []byte(`{"type":"drain"}`)
	msgJoined   = []byte(`{"type":"joined"}`)
)

func challengeMessage(nonce string) []byte {
	return []byte(`{"type":"challenge","nonce":"` + nonce + `"}`)
}

func incomingMessage(token string) []byte {
	return []byte(`{"type":"incoming","token":"` + token + `"}`)
}

// Info is what GET /v1 answers.
type Info struct {
	Relay    string `json:"relay"`
	Protocol int    `json:"protocol"`
	// Regions is always an empty array: protocol 1 clients refuse an answer without it.
	Regions  []json.RawMessage `json:"regions"`
	Features []string          `json:"features"`
	// Version is the relay program's, which clients may show; older relays have none.
	Version string `json:"version,omitempty"`
}

// SocketPath is the socket a request path names.
type SocketPath struct {
	// Kind is "host", "connect" or "accept".
	Kind  string
	ID    string
	Token string
}

// ParseSocketPath reads /v1/hosts/<id>, /v1/hosts/<id>/connect and /v1/hosts/<id>/accept/<token>.
func ParseSocketPath(path string) (SocketPath, bool) {
	rest, ok := strings.CutPrefix(path, "/v1/hosts/")
	if !ok {
		return SocketPath{}, false
	}
	parts := strings.Split(rest, "/")
	if !IsHostID(parts[0]) {
		return SocketPath{}, false
	}
	switch {
	case len(parts) == 1:
		return SocketPath{Kind: "host", ID: parts[0]}, true
	case len(parts) == 2 && parts[1] == "connect":
		return SocketPath{Kind: "connect", ID: parts[0]}, true
	case len(parts) == 3 && parts[1] == "accept" && parts[2] != "":
		return SocketPath{Kind: "accept", ID: parts[0], Token: parts[2]}, true
	}
	return SocketPath{}, false
}
