// Package config reads kettu-relay's settings from its environment.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/KettuAI/relay/link"
)

// Mode is how the relay gets its certificate.
type Mode int

const (
	// Loopback serves plain http on this computer alone, for trying the relay out.
	Loopback Mode = iota
	// Proxy serves plain http behind the user's own https proxy.
	Proxy
	// Domain serves https with a Let's Encrypt certificate for its domain.
	Domain
	// IP serves https with a self-signed certificate that clients pin.
	IP
)

func (m Mode) String() string {
	return [...]string{"loopback", "proxy", "domain", "ip"}[m]
}

// DefaultLocalListen is where the relay listens in Loopback and Proxy modes unless told.
const DefaultLocalListen = "127.0.0.1:8798"

// Config is the relay's settings.
type Config struct {
	Mode Mode
	// Origin is the public origin hosts sign and the link names; "" in Loopback mode, where it
	// is the request's own.
	Origin string
	// Domain is the certificate's name in Domain mode.
	Domain string
	// IP is the address in IP mode.
	IP net.IP
	// Listen is the address the relay serves on.
	Listen string
	// HTTPListen serves ACME's http-01 challenges and the redirect to https in Domain mode; ""
	// for none.
	HTTPListen string
	DataDir    string
	// AccessKey is KETTU_RELAY_ACCESS_KEY, "" when the key file is used.
	AccessKey string
	// ACMEEmail is given to Let's Encrypt for expiry notices; optional.
	ACMEEmail      string
	TrustedProxies []netip.Prefix
	// Web serves the web client, where the mode allows it.
	Web                  bool
	ConnectionsPerMinute int
	DevicesPerMinute     int
}

// FromEnv reads the settings from the environment.
func FromEnv() (Config, error) { return Parse(os.Getenv) }

// Parse reads the settings through getenv.
func Parse(getenv func(string) string) (Config, error) {
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }
	c := Config{
		Listen:    get("KETTU_RELAY_LISTEN"),
		DataDir:   get("KETTU_RELAY_DATA"),
		AccessKey: get("KETTU_RELAY_ACCESS_KEY"),
		ACMEEmail: get("KETTU_RELAY_ACME_EMAIL"),
		Domain:    strings.ToLower(strings.TrimSuffix(get("KETTU_RELAY_DOMAIN"), ".")),
	}
	if c.DataDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return Config{}, errors.New("set KETTU_RELAY_DATA to the relay's data directory")
		}
		c.DataDir = filepath.Join(dir, "kettu-relay")
	}
	publicURL, ipText := get("KETTU_RELAY_URL"), get("KETTU_RELAY_IP")
	set := 0
	for _, v := range []string{c.Domain, publicURL, ipText} {
		if v != "" {
			set++
		}
	}
	if set > 1 {
		return Config{}, errors.New("set only one of KETTU_RELAY_DOMAIN, KETTU_RELAY_URL and KETTU_RELAY_IP")
	}

	switch {
	case c.Domain != "":
		c.Mode = Domain
		if !validDomain(c.Domain) {
			return Config{}, errors.New("KETTU_RELAY_DOMAIN is not a domain name")
		}
		c.Listen = or(c.Listen, ":443")
		c.HTTPListen = or(get("KETTU_RELAY_HTTP_LISTEN"), ":80")
		if c.HTTPListen == "off" {
			c.HTTPListen = ""
		}
		c.Origin = "https://" + hostPort(c.Domain, c.Listen)
	case ipText != "":
		c.Mode = IP
		c.IP = net.ParseIP(ipText)
		if c.IP == nil {
			return Config{}, errors.New("KETTU_RELAY_IP is not an IP address")
		}
		c.Listen = or(c.Listen, ":443")
		c.Origin = "https://" + hostPort(c.IP.String(), c.Listen)
	case publicURL != "":
		c.Mode = Proxy
		origin, err := parseOrigin(publicURL)
		if err != nil {
			return Config{}, err
		}
		c.Origin = origin
		c.Listen = or(c.Listen, DefaultLocalListen)
	default:
		c.Mode = Loopback
		c.Listen = or(c.Listen, DefaultLocalListen)
		host, _, err := net.SplitHostPort(c.Listen)
		if err != nil || !link.Loopback(host) {
			return Config{}, errors.New("without KETTU_RELAY_DOMAIN, KETTU_RELAY_URL or KETTU_RELAY_IP the relay serves plain http, which it does only on a loopback address")
		}
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return Config{}, fmt.Errorf("KETTU_RELAY_LISTEN is not host:port: %v", err)
	}

	proxies := get("KETTU_RELAY_TRUSTED_PROXIES")
	if proxies == "" && c.Mode == Proxy {
		proxies = "127.0.0.0/8,::1/128"
	}
	for _, p := range strings.Split(proxies, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			addr, err2 := netip.ParseAddr(p)
			if err2 != nil {
				return Config{}, fmt.Errorf("KETTU_RELAY_TRUSTED_PROXIES: %q is not an address or a CIDR range", p)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix.Masked())
	}

	switch strings.ToLower(get("KETTU_RELAY_WEB")) {
	case "", "on":
		// A browser cannot pin a certificate, so a relay at an IP address serves no web client.
		c.Web = c.Mode != IP
	case "off":
	default:
		return Config{}, errors.New("KETTU_RELAY_WEB is on or off")
	}

	var err error
	if c.ConnectionsPerMinute, err = number(get, "KETTU_RELAY_CONNECTIONS_PER_MINUTE"); err != nil {
		return Config{}, err
	}
	if c.DevicesPerMinute, err = number(get, "KETTU_RELAY_DEVICES_PER_MINUTE"); err != nil {
		return Config{}, err
	}
	return c, nil
}

func number(get func(string) string, name string) (int, error) {
	v := get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s is a positive number", name)
	}
	return n, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// hostPort names host with the listen port unless it is 443.
func hostPort(host, listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if err != nil || port == "443" || port == "" {
		return host
	}
	return host + ":" + port
}

func validDomain(d string) bool {
	if len(d) > 253 || !strings.Contains(d, ".") || net.ParseIP(d) != nil {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func parseOrigin(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("KETTU_RELAY_URL is the relay's public origin, as https://relay.example.com")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !link.Loopback(u.Hostname()) {
			return "", errors.New("KETTU_RELAY_URL is https: plain http is only for a relay on this computer")
		}
	default:
		return "", errors.New("KETTU_RELAY_URL is https")
	}
	if net.ParseIP(u.Hostname()) != nil && u.Scheme == "https" && !link.Loopback(u.Hostname()) {
		return "", errors.New("behind a proxy KETTU_RELAY_URL names a domain; for an IP address alone use KETTU_RELAY_IP")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// RequestOrigin is the origin hosts sign for a request: the configured one, or in Loopback mode
// the request's own, which names this computer.
func (c Config) RequestOrigin(r *http.Request) string {
	if c.Origin != "" {
		return c.Origin
	}
	return "http://" + strings.ToLower(r.Host)
}

// LinkOrigin is the origin the printed link names.
func (c Config) LinkOrigin() string {
	if c.Origin != "" {
		return c.Origin
	}
	host, port, _ := net.SplitHostPort(c.Listen)
	return "http://" + net.JoinHostPort(host, port)
}

// HasDomain reports whether clients reach the relay by a domain over https, for HSTS.
func (c Config) HasDomain() bool {
	u, err := url.Parse(c.Origin)
	return err == nil && u.Scheme == "https" && net.ParseIP(u.Hostname()) == nil
}

// Address is the client's address for rate limits: the connection's, or, from a trusted proxy,
// the nearest address in X-Forwarded-For that is not a trusted proxy.
func (c Config) Address(r *http.Request) string {
	remote := remoteAddr(r.RemoteAddr)
	if !remote.IsValid() || !c.trusted(remote) {
		return remote.String()
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		addr = addr.Unmap()
		if !c.trusted(addr) {
			return addr.String()
		}
	}
	return remote.String()
}

func (c Config) trusted(addr netip.Addr) bool {
	for _, p := range c.TrustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func remoteAddr(s string) netip.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap()
	}
	addr, _ := netip.ParseAddr(s)
	return addr.Unmap()
}
