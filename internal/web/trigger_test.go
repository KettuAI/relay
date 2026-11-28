package web

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func pageTarball(t *testing.T, label string) []byte {
	t.Helper()
	return tarball(t, []entry{
		{name: "./", typeflag: tar.TypeDir},
		{name: "./index.html", body: "<!doctype html><title>" + label + "</title>"},
		{name: "./assets/", typeflag: tar.TypeDir},
		{name: "./assets/app.js", body: "console.log(" + fmt.Sprintf("%q", label) + ")"},
	})
}

func fastUpdater(g *github, dir string) *Updater {
	host := strings.TrimPrefix(g.server.URL, "http://")
	return &Updater{
		Dir:               dir,
		Release:           g.server.URL + "/release",
		AllowedPrefix:     g.server.URL + "/download/",
		Hosts:             []string{host},
		pollInterval:      10 * time.Millisecond,
		ackTimeout:        120 * time.Millisecond,
		heartbeatInterval: 25 * time.Millisecond,
		staleTimeout:      180 * time.Millisecond,
		resultTimeout:     10 * time.Second,
		orphanMaxAge:      50 * time.Millisecond,
	}
}

func waitForTag(t *testing.T, u *Updater, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if dir, tag := u.Current(); tag == want && dir != "" {
			return dir
		}
		time.Sleep(10 * time.Millisecond)
	}
	dir, tag := u.Current()
	t.Fatalf("timed out waiting for tag %q (got dir=%q tag=%q)", want, dir, tag)
	return ""
}

func fetchBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestTriggerHotReloadUpdatesInPlaceAndPrunesSafely(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.1.30", pageTarball(t, "v0.1.30"))
	webDir := filepath.Join(t.TempDir(), "web")

	relayUpdater := fastUpdater(g, webDir)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	var logs bytes.Buffer
	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(&logs, format+"\n", args...)
	}
	go func() {
		defer close(runDone)
		relayUpdater.Run(ctx, time.Hour, logf)
	}()
	defer func() {
		cancel()
		<-runDone
	}()

	dirV1 := waitForTag(t, relayUpdater, "v0.1.30")
	srv := httptest.NewServer(Handler(relayUpdater, false))
	defer srv.Close()

	if status, body := fetchBody(t, srv.URL+"/"); status != 200 || !strings.Contains(body, "v0.1.30") {
		t.Fatalf("initial serve: %d %q", status, body)
	}

	// Publish v0.1.32 and trigger from a separate Updater instance (simulating the CLI process).
	g.publish("v0.1.32", pageTarball(t, "v0.1.32"))
	cliUpdater := fastUpdater(g, webDir)
	res, err := cliUpdater.TriggerUpdate(context.Background())
	if err != nil {
		t.Fatalf("TriggerUpdate v0.1.32: %v", err)
	}
	if !res.ViaRelay || !res.Updated || res.From != "v0.1.30" || res.To != "v0.1.32" {
		t.Fatalf("unexpected result: %+v", res)
	}
	dirV2, tagV2 := relayUpdater.Current()
	if tagV2 != "v0.1.32" || dirV2 == "" || dirV2 == dirV1 {
		t.Fatalf("relay did not switch in place: dir=%q tag=%q", dirV2, tagV2)
	}
	if status, body := fetchBody(t, srv.URL+"/"); status != 200 || !strings.Contains(body, "v0.1.32") {
		t.Fatalf("after hot reload: %d %q", status, body)
	}
	// Previous copy (v0.1.30) must stay on disk for any in-flight HTTP reads.
	if _, err := os.Stat(filepath.Join(dirV1, "index.html")); err != nil {
		t.Fatalf("previous version dir was pruned too early: %v", err)
	}

	// Publish v0.1.33 and trigger again: v0.1.30 should now be pruned while v0.1.32 stays.
	g.publish("v0.1.33", pageTarball(t, "v0.1.33"))
	res, err = fastUpdater(g, webDir).TriggerUpdate(context.Background())
	if err != nil {
		t.Fatalf("TriggerUpdate v0.1.33: %v", err)
	}
	if !res.ViaRelay || !res.Updated || res.From != "v0.1.32" || res.To != "v0.1.33" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if status, body := fetchBody(t, srv.URL+"/"); status != 200 || !strings.Contains(body, "v0.1.33") {
		t.Fatalf("after second hot reload: %d %q", status, body)
	}
	if _, err := os.Stat(dirV1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("v0.1.30 dir was not pruned on v0.1.33 update: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirV2, "index.html")); err != nil {
		t.Fatalf("v0.1.32 dir was pruned too early: %v", err)
	}

	logMu.Lock()
	logText := logs.String()
	logMu.Unlock()
	if !strings.Contains(logText, "updated from v0.1.30 to v0.1.32") {
		t.Fatalf("expected update log line, got:\n%s", logText)
	}
}

func TestTriggerHotReloadFromEmptyInitialCopy(t *testing.T) {
	g := newGitHub(t)
	g.mu.Lock()
	g.tag, g.assets = "v0.1.29", false
	g.mu.Unlock()

	webDir := filepath.Join(t.TempDir(), "web")
	relayUpdater := fastUpdater(g, webDir)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		relayUpdater.Run(ctx, time.Hour, nil)
	}()
	defer func() {
		cancel()
		<-runDone
	}()

	// Wait briefly for initial Run check to complete with no client (webDir does not exist yet).
	time.Sleep(40 * time.Millisecond)
	if dir, _ := relayUpdater.Current(); dir != "" {
		t.Fatalf("unexpected initial copy: %q", dir)
	}

	g.mu.Lock()
	g.assets = true
	g.mu.Unlock()
	g.publish("v0.1.30", pageTarball(t, "v0.1.30"))

	res, err := fastUpdater(g, webDir).TriggerUpdate(context.Background())
	if err != nil {
		t.Fatalf("TriggerUpdate from empty: %v", err)
	}
	if !res.ViaRelay || !res.Updated || res.From != "" || res.To != "v0.1.30" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, tag := relayUpdater.Current(); tag != "v0.1.30" {
		t.Fatalf("relay serving %q, want v0.1.30", tag)
	}
}

func TestTriggerHotReloadAlreadyUpToDate(t *testing.T) {
	var archiveHits atomic.Int32
	g := newGitHub(t)
	g.publish("v0.1.32", pageTarball(t, "v0.1.32"))

	// Wrap g.server to count archive downloads.
	origHandler := g.server.Config.Handler
	g.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download/"+Archive {
			archiveHits.Add(1)
		}
		origHandler.ServeHTTP(w, r)
	})

	webDir := filepath.Join(t.TempDir(), "web")
	relayUpdater := fastUpdater(g, webDir)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		relayUpdater.Run(ctx, time.Hour, nil)
	}()
	defer func() {
		cancel()
		<-runDone
	}()

	waitForTag(t, relayUpdater, "v0.1.32")
	beforeHits := archiveHits.Load()

	res, err := fastUpdater(g, webDir).TriggerUpdate(context.Background())
	if err != nil {
		t.Fatalf("TriggerUpdate up-to-date: %v", err)
	}
	if !res.ViaRelay || res.Updated || res.From != "v0.1.32" || res.To != "v0.1.32" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got := archiveHits.Load(); got != beforeHits {
		t.Fatalf("archive downloaded again when already up to date: %d vs %d", got, beforeHits)
	}
}

func TestTriggerHotReloadFailureKeepsCurrentCopy(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.1.30", pageTarball(t, "v0.1.30"))
	webDir := filepath.Join(t.TempDir(), "web")

	relayUpdater := fastUpdater(g, webDir)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		relayUpdater.Run(ctx, time.Hour, nil)
	}()
	defer func() {
		cancel()
		<-runDone
	}()

	goodDir := waitForTag(t, relayUpdater, "v0.1.30")
	srv := httptest.NewServer(Handler(relayUpdater, false))
	defer srv.Close()

	// Corrupt the archive for v0.1.32 so SHA-256 verification fails.
	g.publish("v0.1.32", pageTarball(t, "v0.1.32"))
	g.mu.Lock()
	g.archive = append([]byte{}, g.archive...)
	g.archive[len(g.archive)-1] ^= 0xff
	g.mu.Unlock()

	res, err := fastUpdater(g, webDir).TriggerUpdate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum error, got res=%+v err=%v", res, err)
	}
	if !res.ViaRelay {
		t.Fatalf("expected error to come from running relay, got %+v", res)
	}
	if dir, tag := relayUpdater.Current(); dir != goodDir || tag != "v0.1.30" {
		t.Fatalf("failed update altered current copy: dir=%q tag=%q", dir, tag)
	}
	if status, body := fetchBody(t, srv.URL+"/"); status != 200 || !strings.Contains(body, "v0.1.30") {
		t.Fatalf("serve after failed update: %d %q", status, body)
	}

	// ErrNoClient is preserved across the coordination channel.
	g.mu.Lock()
	g.assets = false
	g.mu.Unlock()
	_, err = fastUpdater(g, webDir).TriggerUpdate(context.Background())
	if !errors.Is(err, ErrNoClient) {
		t.Fatalf("expected ErrNoClient, got %v", err)
	}
	if dir, tag := relayUpdater.Current(); dir != goodDir || tag != "v0.1.30" {
		t.Fatalf("ErrNoClient altered current copy: dir=%q tag=%q", dir, tag)
	}
}

func TestTriggerOfflineUpdateAndStaleLockRecovery(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.1.30", pageTarball(t, "v0.1.30"))
	webDir := filepath.Join(t.TempDir(), "web")

	// Seed a stale offline.lock and an old orphan response file left behind by a crashed run.
	controlDir := filepath.Join(webDir, controlDirName)
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staleLock := filepath.Join(controlDir, offlineLock)
	if err := os.WriteFile(staleLock, []byte(`{"id":"0123456789abcdef0123456789abcdef","seq":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	orphanRes := filepath.Join(controlDir, resPrefix+"abcdef0123456789abcdef0123456789"+jsonSuffix)
	if err := os.WriteFile(orphanRes, []byte(`{"id":"abcdef0123456789abcdef0123456789"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(orphanRes, old, old)

	res, err := fastUpdater(g, webDir).TriggerUpdate(context.Background())
	if err != nil {
		t.Fatalf("offline TriggerUpdate v0.1.30: %v", err)
	}
	if res.ViaRelay || !res.Updated || res.From != "" || res.To != "v0.1.30" {
		t.Fatalf("unexpected offline result: %+v", res)
	}
	if _, err := os.Stat(staleLock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("offline lock left behind: %v", err)
	}
	if _, err := os.Stat(orphanRes); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old orphan response was not cleaned: %v", err)
	}

	// Next relay startup loads v0.1.30 straight from disk.
	nextStart := &Updater{Dir: webDir}
	nextStart.Load()
	dirV1, tagV1 := nextStart.Current()
	if dirV1 == "" || tagV1 != "v0.1.30" {
		t.Fatalf("next startup loaded dir=%q tag=%q", dirV1, tagV1)
	}

	// Offline check when already up to date.
	res, err = fastUpdater(g, webDir).TriggerUpdate(context.Background())
	if err != nil {
		t.Fatalf("offline TriggerUpdate up-to-date: %v", err)
	}
	if res.ViaRelay || res.Updated || res.From != "v0.1.30" || res.To != "v0.1.30" {
		t.Fatalf("unexpected up-to-date result: %+v", res)
	}

	// Offline upgrade to v0.1.32.
	g.publish("v0.1.32", pageTarball(t, "v0.1.32"))
	res, err = fastUpdater(g, webDir).TriggerUpdate(context.Background())
	if err != nil {
		t.Fatalf("offline TriggerUpdate v0.1.32: %v", err)
	}
	if res.ViaRelay || !res.Updated || res.From != "v0.1.30" || res.To != "v0.1.32" {
		t.Fatalf("unexpected upgrade result: %+v", res)
	}

	// Failed offline update keeps v0.1.32 on disk.
	g.publish("v0.1.33", pageTarball(t, "v0.1.33"))
	g.mu.Lock()
	g.archive = append([]byte{}, g.archive...)
	g.archive[0] ^= 0xff
	g.mu.Unlock()
	if _, err := fastUpdater(g, webDir).TriggerUpdate(context.Background()); err == nil {
		t.Fatal("corrupt offline update succeeded")
	}
	verify := &Updater{Dir: webDir}
	verify.Load()
	if _, tag := verify.Current(); tag != "v0.1.32" {
		t.Fatalf("failed offline update broke kept copy: %q", tag)
	}
}

func TestTriggerConcurrentPeriodicAndManualUnderRace(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.1.30", pageTarball(t, "v0.1.30"))
	webDir := filepath.Join(t.TempDir(), "web")

	relayUpdater := fastUpdater(g, webDir)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		relayUpdater.Run(ctx, 20*time.Millisecond, nil)
	}()
	defer func() {
		cancel()
		<-runDone
	}()

	waitForTag(t, relayUpdater, "v0.1.30")
	srv := httptest.NewServer(Handler(relayUpdater, false))
	defer srv.Close()

	// Concurrent readers continuously hit the web handler while periodic ticks and CLI triggers run.
	readerCtx, stopReaders := context.WithCancel(context.Background())
	var readerWg sync.WaitGroup
	readerWg.Add(1)
	go func() {
		defer readerWg.Done()
		for readerCtx.Err() == nil {
			resp, err := http.Get(srv.URL + "/")
			if err == nil {
				_, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
		}
	}()

	g.publish("v0.1.31", pageTarball(t, "v0.1.31"))
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n == 3 {
				time.Sleep(15 * time.Millisecond)
				g.publish("v0.1.32", pageTarball(t, "v0.1.32"))
			}
			res, err := fastUpdater(g, webDir).TriggerUpdate(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if !res.ViaRelay {
				errs <- fmt.Errorf("expected ViaRelay=true, got %+v", res)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent trigger failed: %v", err)
	}

	// A final trigger after v0.1.32 is published guarantees the relay ends up on v0.1.32.
	res, err := fastUpdater(g, webDir).TriggerUpdate(context.Background())
	if err != nil {
		t.Fatalf("final trigger: %v", err)
	}
	if !res.ViaRelay || res.To != "v0.1.32" {
		t.Fatalf("expected v0.1.32, got %+v", res)
	}

	stopReaders()
	readerWg.Wait()
}

func TestTriggerWhileSlowDownloadInProgressKeepsHeartbeat(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.1.30", pageTarball(t, "v0.1.30"))
	webDir := filepath.Join(t.TempDir(), "web")

	blockDownload := make(chan struct{})
	downloadStarted := make(chan struct{}, 1)
	var gateActive atomic.Bool

	origHandler := g.server.Config.Handler
	g.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download/"+Archive && gateActive.Load() {
			select {
			case downloadStarted <- struct{}{}:
			default:
			}
			<-blockDownload
		}
		origHandler.ServeHTTP(w, r)
	})

	relayUpdater := fastUpdater(g, webDir)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		relayUpdater.Run(ctx, time.Hour, nil)
	}()
	defer func() {
		cancel()
		<-runDone
	}()

	waitForTag(t, relayUpdater, "v0.1.30")

	g.publish("v0.1.32", pageTarball(t, "v0.1.32"))
	gateActive.Store(true)

	type outcome struct {
		res UpdateResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := fastUpdater(g, webDir).TriggerUpdate(context.Background())
		done <- outcome{res, err}
	}()

	select {
	case <-downloadStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow download never started")
	}

	// Keep the download running longer than both ackTimeout and staleTimeout so active marker
	// heartbeats must keep the CLI waiting without falsely declaring a crash.
	time.Sleep(350 * time.Millisecond)
	close(blockDownload)

	out := <-done
	if out.err != nil {
		t.Fatalf("slow hot-reload failed: %v", out.err)
	}
	if !out.res.ViaRelay || !out.res.Updated || out.res.From != "v0.1.30" || out.res.To != "v0.1.32" {
		t.Fatalf("unexpected result: %+v", out.res)
	}
}

func TestTriggerReportsRelayStopping(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.1.30", pageTarball(t, "v0.1.30"))
	webDir := filepath.Join(t.TempDir(), "web")

	downloadStarted := make(chan struct{}, 1)
	var gateActive atomic.Bool

	origHandler := g.server.Config.Handler
	g.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download/"+Archive && gateActive.Load() {
			select {
			case downloadStarted <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		origHandler.ServeHTTP(w, r)
	})

	relayUpdater := fastUpdater(g, webDir)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		relayUpdater.Run(ctx, time.Hour, nil)
	}()

	waitForTag(t, relayUpdater, "v0.1.30")

	g.publish("v0.1.32", pageTarball(t, "v0.1.32"))
	gateActive.Store(true)

	errCh := make(chan error, 1)
	go func() {
		_, err := fastUpdater(g, webDir).TriggerUpdate(context.Background())
		errCh <- err
	}()

	select {
	case <-downloadStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("download never started")
	}
	cancel()
	<-runDone

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "stopping") {
			t.Fatalf("expected stopping error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CLI did not receive stopping error")
	}
}

func TestTriggerDetectsCrashedRelayStaleActiveMarker(t *testing.T) {
	webDir := filepath.Join(t.TempDir(), "web")
	controlDir := filepath.Join(webDir, controlDirName)
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef0123456789abcdef"
	activePath := filepath.Join(controlDir, activePrefix+id+jsonSuffix)
	resPath := filepath.Join(controlDir, resPrefix+id+jsonSuffix)
	if err := os.WriteFile(activePath, []byte(`{"id":"`+id+`","seq":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	u := &Updater{
		Dir:          webDir,
		pollInterval: 10 * time.Millisecond,
		staleTimeout: 80 * time.Millisecond,
	}
	_, err := u.waitForResult(context.Background(), id, activePath, resPath)
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("expected stale active marker error, got %v", err)
	}
}

func TestTriggerSecurityRejectsSymlinksAndBadControlFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	webDir := filepath.Join(root, "web")
	if err := os.MkdirAll(webDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(webDir, controlDirName)); err != nil {
		t.Fatal(err)
	}
	u := &Updater{Dir: webDir, pollInterval: 10 * time.Millisecond, ackTimeout: 50 * time.Millisecond}
	if _, err := u.TriggerUpdate(context.Background()); err == nil {
		t.Fatal("symlinked .control directory was accepted")
	}

	// parsePrefixedID rejects path traversal and non-hex IDs.
	for _, bad := range []string{
		"req-../escape.json",
		"req-short.json",
		"req-0123456789ABCDEF0123456789ABCDEF.json",
		"other-0123456789abcdef0123456789abcdef.json",
	} {
		if _, ok := parsePrefixedID(bad, reqPrefix); ok {
			t.Errorf("parsePrefixedID accepted %q", bad)
		}
	}
}

func TestTriggerOfflineConcurrentSerializesViaLock(t *testing.T) {
	g := newGitHub(t)
	g.publish("v0.1.32", pageTarball(t, "v0.1.32"))
	webDir := filepath.Join(t.TempDir(), "web")

	var wg sync.WaitGroup
	results := make([]UpdateResult, 3)
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = fastUpdater(g, webDir).TriggerUpdate(context.Background())
		}(i)
	}
	wg.Wait()

	updatedCount := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("offline run %d failed: %v", i, errs[i])
		}
		if results[i].ViaRelay || results[i].To != "v0.1.32" {
			t.Fatalf("offline run %d unexpected result: %+v", i, results[i])
		}
		if results[i].Updated {
			updatedCount++
		}
	}
	if updatedCount != 1 {
		t.Fatalf("expected exactly 1 offline run to download and update, got %d (%+v)", updatedCount, results)
	}
}
