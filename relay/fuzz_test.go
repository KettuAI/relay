package relay

import (
	"encoding/json"
	"testing"
)

func FuzzParseHostMessage(f *testing.F) {
	v := loadVectors(f)
	for _, text := range append(v.HostMessages.Valid, v.HostMessages.Invalid...) {
		f.Add([]byte(text))
	}
	f.Fuzz(func(t *testing.T, text []byte) {
		m, ok := ParseHostMessage(text)
		if !ok {
			return
		}
		switch m.Type {
		case "sign-in":
			if len(m.PublicKey) != 32 || len(m.Signature) != 64 || !isCredential(m.Credential) {
				t.Fatalf("taken: %q", text)
			}
		case "token":
			if !isCredential(m.Credential) {
				t.Fatalf("taken: %q", text)
			}
		case "profile":
			if m.Profile != nil && (!isProfileName(m.Profile.Name) || !platformPattern.MatchString(m.Profile.Platform)) {
				t.Fatalf("taken: %q", text)
			}
		default:
			t.Fatalf("type %q from %q", m.Type, text)
		}
	})
}

func FuzzParseDeviceMessage(f *testing.F) {
	v := loadVectors(f)
	for _, text := range append(v.DeviceMessages.Valid, v.DeviceMessages.Invalid...) {
		f.Add([]byte(text))
	}
	f.Fuzz(func(t *testing.T, text []byte) {
		m, ok := ParseDeviceMessage(text)
		if !ok {
			return
		}
		if !IsConnect(text) || len(m.Credential) > maxCredential || (!m.HasCredential && m.Credential != "") {
			t.Fatalf("taken: %q as %+v", text, m)
		}
		for _, feature := range m.Features {
			if feature != FeatureDrain {
				t.Fatalf("feature %q", feature)
			}
		}
		var fields map[string]any
		if json.Unmarshal(text, &fields) != nil {
			t.Fatalf("not JSON but taken: %q", text)
		}
	})
}

func FuzzParseSocketPath(f *testing.F) {
	v := loadVectors(f)
	for _, p := range []string{v.Paths.Info, v.Paths.Host, v.Paths.Connect, v.Paths.Accept, "/v1/hosts//connect"} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, p string) {
		path, ok := ParseSocketPath(p)
		if !ok {
			return
		}
		want := "/v1/hosts/" + path.ID
		switch path.Kind {
		case "connect":
			want += "/connect"
		case "accept":
			want += "/accept/" + path.Token
		}
		if !IsHostID(path.ID) || want != p {
			t.Fatalf("%q read as %+v", p, path)
		}
	})
}
