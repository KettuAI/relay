package certs

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPinIsWhatClientsSee serves the certificate and checks the pin against the one a client
// computes from what the server shows: the SHA-256 of its SubjectPublicKeyInfo.
func TestPinIsWhatClientsSee(t *testing.T) {
	dir := t.TempDir()
	ip := net.ParseIP("127.0.0.1")
	cert, pin, err := SelfSigned(dir, ip)
	if err != nil {
		t.Fatal(err)
	}
	if len(pin) != 43 {
		t.Fatalf("pin %q", pin)
	}
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	defer server.Close()
	conn, err := tls.Dial("tcp", server.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	shown := conn.ConnectionState().PeerCertificates[0]
	sum := sha256.Sum256(shown.RawSubjectPublicKeyInfo)
	if base64.RawURLEncoding.EncodeToString(sum[:]) != pin {
		t.Fatal("the pin is not the shown certificate's")
	}

	_, again, err := SelfSigned(dir, ip)
	if err != nil || again != pin {
		t.Fatal("the certificate changed across restarts")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(dir, keyFile))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode %v", info.Mode().Perm())
		}
	}
	_, other, err := SelfSigned(dir, net.ParseIP("203.0.113.7"))
	if err != nil || other == pin {
		t.Fatal("another address kept the old certificate")
	}
}
