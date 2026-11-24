package link

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

// The key is made per run so no access key is ever written into the repository.
var key = func() string {
	b := make([]byte, 32)
	rand.Read(b)
	return "krk_" + base64.RawURLEncoding.EncodeToString(b)
}()

const pin = "A1l5acSz3nk-OfVkdXoabfGkc-1e1RuvNnxBpGujxxw"

func TestFormat(t *testing.T) {
	for l, want := range map[Link]string{
		{Origin: "https://relay.example.com", Key: key}:            "https://relay.example.com#key=" + key,
		{Origin: "https://203.0.113.7", Key: key, Pin: pin}:        "https://203.0.113.7#key=" + key + "&pin=" + pin,
		{Origin: "https://[2001:db8::7]:7443", Key: key, Pin: pin}: "https://[2001:db8::7]:7443#key=" + key + "&pin=" + pin,
		{Origin: "http://127.0.0.1:8798", Key: key}:                "http://127.0.0.1:8798#key=" + key,
	} {
		if l.String() != want {
			t.Errorf("%+v prints %s, want %s", l, l, want)
		}
		got, err := Parse(want)
		if err != nil || got != l {
			t.Errorf("%s reads as %+v, %v", want, got, err)
		}
	}
}

func TestParse(t *testing.T) {
	ok := map[string]Link{
		"https://Relay.Example.com/#key=" + key + "&other=1":           {Origin: "https://relay.example.com", Key: key},
		" https://relay.example.com#pin=" + pin + "&key=" + key + "\n": {Origin: "https://relay.example.com", Key: key, Pin: pin},
		"http://localhost:8798#key=" + key:                             {Origin: "http://localhost:8798", Key: key},
	}
	for s, want := range ok {
		if got, err := Parse(s); err != nil || got != want {
			t.Errorf("%q: %+v %v", s, got, err)
		}
	}
	for _, s := range []string{
		"https://relay.example.com",
		"https://relay.example.com#key=krk_short",
		"https://relay.example.com#key=" + strings.TrimPrefix(key, "krk_"),
		"http://relay.example.com#key=" + key,
		"https://203.0.113.7#key=" + key,
		"https://203.0.113.7#key=" + key + "&pin=AAAA",
		"https://relay.example.com/path#key=" + key,
		"https://relay.example.com?x=1#key=" + key,
		"https://user@relay.example.com#key=" + key,
		"wss://relay.example.com#key=" + key,
		"https://relay.example.com:99999#key=" + key,
	} {
		if l, err := Parse(s); err == nil {
			t.Errorf("%q read as %+v", s, l)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add("https://relay.example.com#key=" + key)
	f.Add("https://203.0.113.7:7443#key=" + key + "&pin=" + pin)
	f.Add("http://[::1]:8798#key=" + key)
	f.Fuzz(func(t *testing.T, s string) {
		l, err := Parse(s)
		if err != nil {
			return
		}
		again, err := Parse(l.String())
		if err != nil || again != l {
			t.Fatalf("%q read as %+v, which reads back as %+v, %v", s, l, again, err)
		}
		if strings.ContainsAny(l.Origin, "#?@ ") {
			t.Fatalf("origin %q", l.Origin)
		}
	})
}

func TestMasked(t *testing.T) {
	l := Link{Origin: "https://203.0.113.7", Key: key, Pin: pin}
	got := l.Masked()
	if strings.Contains(got, key[4:len(key)-4]) || !strings.HasSuffix(got, "…"+key[len(key)-4:]+"&pin="+pin) {
		t.Fatalf("masked link %q", got)
	}
}
