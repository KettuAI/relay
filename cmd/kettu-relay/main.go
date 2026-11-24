// Command kettu-relay is a Kettu relay for your own server: Kettu computers sign in to it with its
// access key, and devices reach them through it. It passes only the end-to-end encrypted channel
// between them.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/KettuAI/relay/internal/accesskey"
	"github.com/KettuAI/relay/internal/certs"
	"github.com/KettuAI/relay/internal/config"
	"github.com/KettuAI/relay/internal/web"
	"github.com/KettuAI/relay/link"
	"github.com/KettuAI/relay/relay"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `kettu-relay: a Kettu relay for your own server.

Usage:
  kettu-relay            run the relay
  kettu-relay link       print the link to paste into Kettu again
  kettu-relay key reset  make a new access key; computers signed in with the old one are cut off
  kettu-relay version    print the version

The relay is set up with environment variables; see the README.
`

func main() {
	args := os.Args[1:]
	var err error
	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "run"):
		err = run()
	case len(args) == 1 && args[0] == "link":
		err = printLink()
	case len(args) == 2 && args[0] == "key" && args[1] == "reset":
		err = resetKey()
	case len(args) == 1 && (args[0] == "version" || args[0] == "--version"):
		fmt.Println(version)
	case len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help"):
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kettu-relay:", err)
		os.Exit(1)
	}
}

// setup is what every command reads: the settings, the key and, at an IP address, the pin.
type setup struct {
	cfg   config.Config
	store *accesskey.Store
	pin   string
	cert  *tls.Certificate
}

func load(logf func(string, ...any)) (setup, error) {
	cfg, err := config.FromEnv()
	if err != nil {
		return setup{}, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return setup{}, err
	}
	store, created, err := accesskey.Load(cfg.DataDir, cfg.AccessKey)
	if err != nil {
		return setup{}, err
	}
	if created && logf != nil {
		logf("made a new access key in %s", filepath.Join(cfg.DataDir, accesskey.FileName))
	}
	s := setup{cfg: cfg, store: store}
	if cfg.Mode == config.IP {
		cert, pin, err := certs.SelfSigned(filepath.Join(cfg.DataDir, "tls"), cfg.IP)
		if err != nil {
			return setup{}, fmt.Errorf("self-signed certificate: %w", err)
		}
		s.cert, s.pin = &cert, pin
	}
	return s, nil
}

func (s setup) link() link.Link {
	return link.Link{Origin: s.cfg.LinkOrigin(), Key: s.store.Key(), Pin: s.pin}
}

func printLink() error {
	s, err := load(nil)
	if err != nil {
		return err
	}
	fmt.Println(s.link())
	return nil
}

func resetKey() error {
	s, err := load(nil)
	if err != nil {
		return err
	}
	if _, err := s.store.Reset(); err != nil {
		return err
	}
	fmt.Println("Made a new access key. A running relay cuts off computers signed in with the old one within a few seconds.")
	fmt.Println("Paste the new link into Kettu on each computer:")
	fmt.Println(s.link())
	return nil
}

func run() error {
	logger := log.New(os.Stderr, "", log.LstdFlags)
	logf := logger.Printf
	s, err := load(logf)
	if err != nil {
		return err
	}
	cfg := s.cfg
	admission := accesskey.Admission{Store: s.store}

	updater := &web.Updater{Dir: filepath.Join(cfg.DataDir, "web")}
	if cfg.Web {
		updater.Load()
	}
	serving := func() bool {
		dir, _ := updater.Current()
		return cfg.Web && dir != ""
	}
	r, err := relay.New(relay.Options{
		Admission: admission,
		Origin:    cfg.RequestOrigin,
		Address:   cfg.Address,
		Features: func() []string {
			features := []string{relay.FeatureAccessKey, relay.FeatureDrain}
			if serving() {
				features = append(features, relay.FeatureWeb)
			}
			return features
		},
		Version:              version,
		ConnectionsPerMinute: cfg.ConnectionsPerMinute,
		DevicesPerMinute:     cfg.DevicesPerMinute,
	})
	if err != nil {
		return err
	}
	pages := web.Handler(updater, cfg.HasDomain())
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.Handles(req.URL.Path) {
			r.ServeHTTP(w, req)
			return
		}
		if !cfg.Web {
			http.NotFound(w, req)
			return
		}
		pages.ServeHTTP(w, req)
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// net/http's own error log names client addresses, which the relay does not keep.
	quiet := log.New(io.Discard, "", 0)
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          quiet,
		Protocols:         &protocols,
	}
	var challenges *http.Server
	switch cfg.Mode {
	case config.Domain:
		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.Domain),
			Cache:      autocert.DirCache(filepath.Join(cfg.DataDir, "acme")),
			Email:      cfg.ACMEEmail,
		}
		tlsConfig := manager.TLSConfig()
		// WebSockets here need HTTP/1.1; acme-tls/1 answers Let's Encrypt's TLS-ALPN challenge.
		tlsConfig.NextProtos = []string{"http/1.1", acme.ALPNProto}
		tlsConfig.MinVersion = tls.VersionTLS12
		server.TLSConfig = tlsConfig
		if cfg.HTTPListen != "" {
			challenges = &http.Server{
				Addr:              cfg.HTTPListen,
				Handler:           manager.HTTPHandler(nil),
				ReadHeaderTimeout: 10 * time.Second,
				ErrorLog:          quiet,
			}
		}
	case config.IP:
		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{*s.cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"http/1.1"},
		}
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	serveErr := make(chan error, 2)
	go func() {
		if server.TLSConfig != nil {
			serveErr <- server.ServeTLS(listener, "", "")
		} else {
			serveErr <- server.Serve(listener)
		}
	}()
	if challenges != nil {
		go func() {
			if err := challenges.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logf("ACME http-01 challenges are not served on %s (%v); the TLS-ALPN challenge on %s still works", cfg.HTTPListen, err, cfg.Listen)
			}
		}()
	}

	logf("kettu-relay %s serving %s mode on %s", version, cfg.Mode, cfg.Listen)
	switch cfg.Mode {
	case config.Domain:
		logf("certificate for %s comes from Let's Encrypt on the first https request", cfg.Domain)
	case config.IP:
		logf("self-signed certificate for %s, pin %s", cfg.IP, s.pin)
	case config.Loopback:
		logf("plain http on this computer only; set KETTU_RELAY_DOMAIN, KETTU_RELAY_URL or KETTU_RELAY_IP to serve other computers")
	}
	switch {
	case !cfg.Web && cfg.Mode == config.IP:
		logf("web client: not served at an IP address, since browsers cannot pin a certificate")
	case !cfg.Web:
		logf("web client: off")
	default:
		if _, tag := updater.Current(); tag != "" {
			logf("web client: serving %s, kept from before", tag)
		}
		go updater.Run(ctx, 24*time.Hour, logf)
	}
	go s.store.Watch(ctx, 2*time.Second, func() {
		logf("the access key changed; computers signed in with the old one are cut off")
		r.Revoke(admission.Stale)
	})

	fmt.Printf("\nPaste this link into Kettu, in Settings > Remote access > Your own relay:\n\n  %s\n\n", s.link())

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	logf("stopping: telling computers and devices to move")
	drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.Shutdown(drain)
	server.Shutdown(drain)
	if challenges != nil {
		challenges.Shutdown(drain)
	}
	return nil
}
