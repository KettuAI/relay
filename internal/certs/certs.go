// Package certs makes and keeps the self-signed certificate of a relay reached by IP address,
// which clients trust by its pin instead of a certificate authority.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	certFile = "ip-cert.pem"
	keyFile  = "ip-key.pem"
	validFor = 10 * 365 * 24 * time.Hour
)

// Pin is what clients pin a relay at an IP address by: the base64url SHA-256, without padding,
// of the certificate's SubjectPublicKeyInfo in DER.
func Pin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// SelfSigned loads the certificate for ip kept in dir, or makes and keeps a new one, so the pin
// stays the same across restarts.
func SelfSigned(dir string, ip net.IP) (tls.Certificate, string, error) {
	if cert, pin, err := load(dir, ip); err == nil {
		return cert, pin, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: ip.String()},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{ip},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if err := write(filepath.Join(dir, keyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := write(filepath.Join(dir, certFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return tls.Certificate{}, "", err
	}
	return load(dir, ip)
}

func load(dir string, ip net.IP) (tls.Certificate, string, error) {
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if time.Until(leaf.NotAfter) < 30*24*time.Hour {
		return tls.Certificate{}, "", errors.New("certificate expires soon")
	}
	for _, have := range leaf.IPAddresses {
		if have.Equal(ip) {
			pair.Leaf = leaf
			return pair, Pin(leaf), nil
		}
	}
	return tls.Certificate{}, "", errors.New("certificate is for another address")
}

func write(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cert-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
