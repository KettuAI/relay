// Package link reads and writes the link kettu-relay prints for Kettu's settings: the relay's
// origin with its access key, and for a relay at an IP address its certificate pin, after `#`,
// as in https://relay.example.com#key=krk_... or https://203.0.113.7#key=krk_...&pin=....
package link

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"strings"
)

// Link is a relay's address and what a host needs to sign in to it.
type Link struct {
	// Origin is the relay's https origin, or http on a loopback address.
	Origin string
	// Key is the relay's access key.
	Key string
	// Pin is the certificate pin of a relay at an IP address, else "".
	Pin string
}

// String is the link as kettu-relay prints it.
func (l Link) String() string {
	s := l.Origin + "#key=" + l.Key
	if l.Pin != "" {
		s += "&pin=" + l.Pin
	}
	return s
}

// Parse reads a link. It takes nothing but an origin before `#`, takes key and pin after it and
// ignores other fields, and asks for a pin exactly when an https relay is at an IP address.
func Parse(s string) (Link, error) {
	origin, fragment, ok := strings.Cut(strings.TrimSpace(s), "#")
	if !ok {
		return Link{}, errors.New("the link has no key after #")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.ForceQuery {
		return Link{}, errors.New("the link does not start with a relay's address")
	}
	if u.Path != "" && u.Path != "/" {
		return Link{}, errors.New("the relay's address has a path")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	switch u.Scheme {
	case "https":
	case "http":
		if !Loopback(host) {
			return Link{}, errors.New("plain http is only for a relay on this computer")
		}
	default:
		return Link{}, errors.New("the relay's address is not https")
	}
	if port := u.Port(); port != "" && !validPort(port) {
		return Link{}, errors.New("the relay's port is not a port")
	}
	fields, err := url.ParseQuery(fragment)
	if err != nil {
		return Link{}, errors.New("the part after # is not key=krk_ and the key")
	}
	l := Link{Origin: u.Scheme + "://" + strings.ToLower(u.Host), Key: fields.Get("key"), Pin: fields.Get("pin")}
	if !validKey(l.Key) {
		return Link{}, errors.New("the link has no access key")
	}
	if l.Pin != "" && !ValidPin(l.Pin) {
		return Link{}, errors.New("the pin is not a certificate pin")
	}
	if u.Scheme == "https" && ip != nil && l.Pin == "" {
		return Link{}, errors.New("a relay at an IP address needs its pin")
	}
	return l, nil
}

func validKey(s string) bool {
	rest, ok := strings.CutPrefix(s, "krk_")
	if !ok {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == rest
}

// ValidPin reports whether s is a pin: 32 bytes in base64url without padding.
func ValidPin(s string) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}

func validPort(s string) bool {
	if len(s) > 5 {
		return false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n > 0 && n < 65536
}

// Loopback reports whether host names this computer: localhost or a loopback address.
func Loopback(host string) bool {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
