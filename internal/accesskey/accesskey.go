// Package accesskey is kettu-relay's admission: hosts sign in with the relay's one access key,
// and devices bring nothing, since the host itself decides inside the encrypted channel which
// devices it talks to.
package accesskey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KettuAI/relay/relay"
)

// Prefix starts every access key.
const Prefix = "krk_"

// FileName is the key's file in the data directory.
const FileName = "access-key"

// ErrFromEnv is returned by Reset when the key is set in the environment.
var ErrFromEnv = errors.New("the access key is set by KETTU_RELAY_ACCESS_KEY; change it there")

// New makes a key: the prefix and 32 random bytes in base64url.
func New() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(b)
}

// Valid reports whether s is an access key as New makes them.
func Valid(s string) bool {
	rest, ok := strings.CutPrefix(s, Prefix)
	if !ok {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == rest
}

// Store holds the current key, from the environment or the key file.
type Store struct {
	path    string
	fromEnv bool

	mu         sync.Mutex
	key        string
	generation uint64
}

// Load takes the key from env when it is set, else from the data directory's key file, which it
// makes on first start; created reports that it did.
func Load(dataDir, env string) (store *Store, created bool, err error) {
	store = &Store{path: filepath.Join(dataDir, FileName)}
	if env != "" {
		if !Valid(env) {
			return nil, false, errors.New("KETTU_RELAY_ACCESS_KEY is not an access key: krk_ and 43 characters of base64url")
		}
		store.key, store.fromEnv = env, true
		return store, false, nil
	}
	key, err := readKey(store.path)
	if errors.Is(err, os.ErrNotExist) {
		key = New()
		if err := writeKey(store.path, key); err != nil {
			return nil, false, err
		}
		created = true
	} else if err != nil {
		return nil, false, err
	}
	store.key = key
	return store, created, nil
}

func readKey(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(b))
	if !Valid(key) {
		return "", fmt.Errorf("%s does not hold an access key", path)
	}
	return key, nil
}

// writeKey replaces the file at once, readable by its owner alone.
func writeKey(path, key string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".access-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(key + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Key is the current key.
func (s *Store) Key() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.key
}

// Reset writes a new key to the key file; the running relay picks it up with Reload.
func (s *Store) Reset() (string, error) {
	if s.fromEnv {
		return "", ErrFromEnv
	}
	key := New()
	if err := writeKey(s.path, key); err != nil {
		return "", err
	}
	s.set(key)
	return key, nil
}

// Reload reads the key file again and reports whether the key changed.
func (s *Store) Reload() (bool, error) {
	if s.fromEnv {
		return false, nil
	}
	key, err := readKey(s.path)
	if err != nil {
		return false, err
	}
	return s.set(key), nil
}

func (s *Store) set(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == s.key {
		return false
	}
	s.key = key
	s.generation++
	return true
}

// Watch reloads the key file every interval until ctx is done and calls changed after each
// change, so `kettu-relay key reset` from another process reaches the running relay.
func (s *Store) Watch(ctx context.Context, interval time.Duration, changed func()) {
	if s.fromEnv {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ok, err := s.Reload(); err == nil && ok {
				changed()
			}
		}
	}
}

type grant struct{ generation uint64 }

func (grant) Deadline() time.Time { return time.Time{} }

// Admission admits hosts that bring the store's key.
type Admission struct{ Store *Store }

func (a Admission) check(credential string) (relay.Grant, error) {
	a.Store.mu.Lock()
	key, generation := a.Store.key, a.Store.generation
	a.Store.mu.Unlock()
	// Hashing first keeps the comparison's time the same whatever the credential's length.
	given, want := sha256.Sum256([]byte(credential)), sha256.Sum256([]byte(key))
	if subtle.ConstantTimeCompare(given[:], want[:]) != 1 {
		return nil, relay.ErrUnauthorized
	}
	return grant{generation}, nil
}

// Host admits a host whose sign-in token is the key.
func (a Admission) Host(_ context.Context, _ string, credential string) (relay.Grant, error) {
	return a.check(credential)
}

// Renew admits a token message that brings the current key.
func (a Admission) Renew(_ context.Context, _ string, _ relay.Grant, credential string) (relay.Grant, error) {
	return a.check(credential)
}

// Device admits every device, with or without a token: the host checks devices itself.
func (Admission) Device(context.Context, string, relay.DeviceMessage, relay.Grant) error {
	return nil
}

// Stale reports whether a grant was given for an earlier key.
func (a Admission) Stale(g relay.Grant) bool {
	given, ok := g.(grant)
	a.Store.mu.Lock()
	defer a.Store.mu.Unlock()
	return !ok || given.generation != a.Store.generation
}
