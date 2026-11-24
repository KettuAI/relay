package config

import (
	"net/http"
	"testing"
	"time"
)

func parse(env map[string]string) (Config, error) {
	if _, ok := env["KETTU_RELAY_DATA"]; !ok {
		env["KETTU_RELAY_DATA"] = "/tmp/kettu-relay-test"
	}
	return Parse(func(name string) string { return env[name] })
}

func TestModes(t *testing.T) {
	for _, c := range []struct {
		env    map[string]string
		mode   Mode
		origin string
		listen string
		web    bool
	}{
		{map[string]string{}, Loopback, "", DefaultLocalListen, true},
		{map[string]string{"KETTU_RELAY_DOMAIN": "Relay.Example.com."}, Domain, "https://relay.example.com", ":443", true},
		{map[string]string{"KETTU_RELAY_DOMAIN": "relay.example.com", "KETTU_RELAY_LISTEN": ":8443"}, Domain, "https://relay.example.com:8443", ":8443", true},
		{map[string]string{"KETTU_RELAY_URL": "https://relay.example.com/", "KETTU_RELAY_WEB": "off"}, Proxy, "https://relay.example.com", DefaultLocalListen, false},
		{map[string]string{"KETTU_RELAY_IP": "203.0.113.7"}, IP, "https://203.0.113.7", ":443", false},
		{map[string]string{"KETTU_RELAY_IP": "2001:db8::7", "KETTU_RELAY_LISTEN": "[::]:7443"}, IP, "https://[2001:db8::7]:7443", "[::]:7443", false},
	} {
		got, err := parse(c.env)
		if err != nil {
			t.Errorf("%v: %v", c.env, err)
			continue
		}
		if got.Mode != c.mode || got.Origin != c.origin || got.Listen != c.listen || got.Web != c.web {
			t.Errorf("%v: %v %q %q web=%v", c.env, got.Mode, got.Origin, got.Listen, got.Web)
		}
	}
}

func TestRefused(t *testing.T) {
	for _, env := range []map[string]string{
		{"KETTU_RELAY_LISTEN": "0.0.0.0:8798"},
		{"KETTU_RELAY_URL": "http://relay.example.com"},
		{"KETTU_RELAY_URL": "https://relay.example.com/path"},
		{"KETTU_RELAY_URL": "https://203.0.113.7"},
		{"KETTU_RELAY_DOMAIN": "relay.example.com", "KETTU_RELAY_IP": "203.0.113.7"},
		{"KETTU_RELAY_DOMAIN": "203.0.113.7"},
		{"KETTU_RELAY_IP": "relay.example.com"},
		{"KETTU_RELAY_TRUSTED_PROXIES": "nonsense"},
		{"KETTU_RELAY_WEB": "maybe"},
		{"KETTU_RELAY_DEVICES_PER_MINUTE": "-1"},
		{"KETTU_RELAY_URL": "http://localhost:8080", "KETTU_RELAY_LISTEN": "0.0.0.0:8798"},
		{"KETTU_RELAY_MAX_CONNECTIONS": "lots"},
		{"KETTU_RELAY_JOIN_TIMEOUT": "-5"},
	} {
		if _, err := parse(env); err == nil {
			t.Errorf("%v is taken", env)
		}
	}
}

func TestOrigins(t *testing.T) {
	loopback, _ := parse(map[string]string{})
	req, _ := http.NewRequest("GET", "http://localhost:8798/v1", nil)
	if got := loopback.RequestOrigin(req); got != "http://localhost:8798" {
		t.Errorf("loopback origin %q", got)
	}
	if got := loopback.LinkOrigin(); got != "http://127.0.0.1:8798" {
		t.Errorf("loopback link %q", got)
	}
	proxy, _ := parse(map[string]string{"KETTU_RELAY_URL": "https://relay.example.com"})
	if got := proxy.RequestOrigin(req); got != "https://relay.example.com" {
		t.Errorf("proxy origin %q", got)
	}
	if !proxy.HasDomain() || loopback.HasDomain() {
		t.Error("HSTS for the wrong relay")
	}
	ip, _ := parse(map[string]string{"KETTU_RELAY_IP": "203.0.113.7"})
	if ip.HasDomain() {
		t.Error("HSTS at an IP address")
	}
}

func TestForwardedAddresses(t *testing.T) {
	proxy, _ := parse(map[string]string{"KETTU_RELAY_URL": "https://relay.example.com"})
	direct, _ := parse(map[string]string{"KETTU_RELAY_TRUSTED_PROXIES": "10.0.0.0/8"})
	for _, c := range []struct {
		cfg    Config
		remote string
		xff    []string
		want   string
	}{
		{proxy, "127.0.0.1:5000", []string{"198.51.100.9"}, "198.51.100.9"},
		{proxy, "127.0.0.1:5000", []string{"203.0.113.1, 198.51.100.9"}, "198.51.100.9"},
		{proxy, "127.0.0.1:5000", nil, "127.0.0.1"},
		{proxy, "192.0.2.5:5000", []string{"198.51.100.9"}, "192.0.2.5"},
		{direct, "10.1.2.3:5000", []string{"198.51.100.9", "10.4.4.4"}, "198.51.100.9"},
		{direct, "[::ffff:192.0.2.5]:5000", []string{"198.51.100.9"}, "192.0.2.5"},
	} {
		req, _ := http.NewRequest("GET", "https://relay.example.com/v1", nil)
		req.RemoteAddr = c.remote
		for _, v := range c.xff {
			req.Header.Add("X-Forwarded-For", v)
		}
		if got := c.cfg.Address(req); got != c.want {
			t.Errorf("%s %v: %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestLimitsAndWarnings(t *testing.T) {
	c, err := parse(map[string]string{
		"KETTU_RELAY_MAX_CONNECTIONS":             "100",
		"KETTU_RELAY_MAX_CONNECTIONS_PER_ADDRESS": "7",
		"KETTU_RELAY_MAX_PENDING_DEVICES":         "3",
		"KETTU_RELAY_JOIN_TIMEOUT":                "4",
	})
	if err != nil || c.MaxConnections != 100 || c.MaxConnectionsPerAddress != 7 || c.MaxPendingDevices != 3 || c.JoinTimeout != 4*time.Second {
		t.Fatalf("%+v %v", c, err)
	}
	quiet, _ := parse(map[string]string{"KETTU_RELAY_URL": "https://relay.example.com"})
	open, _ := parse(map[string]string{"KETTU_RELAY_URL": "https://relay.example.com", "KETTU_RELAY_LISTEN": "0.0.0.0:8798"})
	if len(quiet.Warnings) != 0 || len(open.Warnings) != 1 {
		t.Fatalf("warnings %v %v", quiet.Warnings, open.Warnings)
	}
	if _, err := parse(map[string]string{"KETTU_RELAY_URL": "http://localhost:8080"}); err != nil {
		t.Fatalf("an http URL on loopback: %v", err)
	}
}

func TestAdmittedOnlyFromTrustedProxies(t *testing.T) {
	proxy, _ := parse(map[string]string{"KETTU_RELAY_URL": "https://relay.example.com", "KETTU_RELAY_LISTEN": "0.0.0.0:8798", "KETTU_RELAY_TRUSTED_PROXIES": "10.0.0.0/8"})
	domain, _ := parse(map[string]string{"KETTU_RELAY_DOMAIN": "relay.example.com"})
	for _, c := range []struct {
		cfg    Config
		remote string
		want   bool
	}{
		{proxy, "10.2.3.4:5000", true},
		{proxy, "192.0.2.5:5000", false},
		{proxy, "127.0.0.1:5000", false},
		{domain, "192.0.2.5:5000", true},
	} {
		req, _ := http.NewRequest("GET", "https://relay.example.com/v1", nil)
		req.RemoteAddr = c.remote
		if got := c.cfg.Admitted(req); got != c.want {
			t.Errorf("%s: admitted %v", c.remote, got)
		}
	}
}
