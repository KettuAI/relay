package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KettuAI/relay/internal/private"
)

const (
	controlDirName = ".control"
	reqPrefix      = "req-"
	activePrefix   = "active-"
	resPrefix      = "res-"
	jsonSuffix     = ".json"
	offlineLock    = "offline.lock"

	defaultPollInterval      = 150 * time.Millisecond
	defaultAckTimeout        = 2 * time.Second
	defaultHeartbeatInterval = 2 * time.Second
	defaultStaleTimeout      = 15 * time.Second
	defaultResultTimeout     = 10 * time.Minute
	defaultOrphanMaxAge      = 10 * time.Minute

	maxControlFileSize = 64 << 10
)

type triggerRequest struct {
	ID string `json:"id"`
}

type activeMarker struct {
	ID  string `json:"id"`
	Seq uint64 `json:"seq"`
}

type triggerResponse struct {
	ID      string `json:"id"`
	Updated bool   `json:"updated"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (u *Updater) getPollInterval() time.Duration {
	if u.pollInterval > 0 {
		return u.pollInterval
	}
	return defaultPollInterval
}

func (u *Updater) getAckTimeout() time.Duration {
	if u.ackTimeout > 0 {
		return u.ackTimeout
	}
	return defaultAckTimeout
}

func (u *Updater) getHeartbeatInterval() time.Duration {
	if u.heartbeatInterval > 0 {
		return u.heartbeatInterval
	}
	return defaultHeartbeatInterval
}

func (u *Updater) getStaleTimeout() time.Duration {
	if u.staleTimeout > 0 {
		return u.staleTimeout
	}
	return defaultStaleTimeout
}

func (u *Updater) getResultTimeout() time.Duration {
	if u.resultTimeout > 0 {
		return u.resultTimeout
	}
	return defaultResultTimeout
}

func (u *Updater) getOrphanMaxAge() time.Duration {
	if u.orphanMaxAge > 0 {
		return u.orphanMaxAge
	}
	return defaultOrphanMaxAge
}

// TriggerUpdate asks a running relay sharing u.Dir to check for and apply a web client update in
// place without restarting or dropping connections. If no running relay claims the request within
// the handshake window, it performs the update directly on disk so the next start serves it.
func (u *Updater) TriggerUpdate(ctx context.Context) (UpdateResult, error) {
	if strings.TrimSpace(u.Dir) == "" {
		return UpdateResult{}, errors.New("web client directory is not set")
	}
	ctx, cancel := context.WithTimeout(ctx, u.getResultTimeout())
	defer cancel()

	controlDir, err := u.ensureControlDir()
	if err != nil {
		return UpdateResult{}, err
	}
	id, err := newRequestID()
	if err != nil {
		return UpdateResult{}, err
	}
	reqPath := filepath.Join(controlDir, reqPrefix+id+jsonSuffix)
	activePath := filepath.Join(controlDir, activePrefix+id+jsonSuffix)
	resPath := filepath.Join(controlDir, resPrefix+id+jsonSuffix)

	reqBytes, err := json.Marshal(triggerRequest{ID: id})
	if err != nil {
		return UpdateResult{}, err
	}
	if err := writeAtomicFile(reqPath, ".req-*", reqBytes); err != nil {
		return UpdateResult{}, err
	}

	claimed, res, done, err := u.waitForClaim(ctx, id, reqPath, activePath, resPath)
	if done {
		return res, err
	}
	if err != nil {
		return UpdateResult{}, err
	}
	if claimed {
		return u.waitForResult(ctx, id, activePath, resPath)
	}
	return u.runOfflineUpdate(ctx, controlDir, id)
}

func (u *Updater) ensureControlDir() (string, error) {
	if _, err := private.Dir(u.Dir); err != nil {
		return "", err
	}
	controlDir := filepath.Join(u.Dir, controlDirName)
	if _, err := private.Dir(controlDir); err != nil {
		return "", err
	}
	return controlDir, nil
}

// waitForClaim waits up to ackTimeout for a running relay to atomically rename reqPath to
// activePath (or finish directly with resPath). If none answers, it atomically removes reqPath so
// the caller can safely fall back to an offline update without racing a late claim.
func (u *Updater) waitForClaim(ctx context.Context, id, reqPath, activePath, resPath string) (claimed bool, res UpdateResult, done bool, err error) {
	deadline := time.Now().Add(u.getAckTimeout())
	poll := u.getPollInterval()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		if r, ok, err := readResponse(resPath, id); err != nil {
			return false, UpdateResult{}, false, err
		} else if ok {
			return true, r.result(), true, r.err()
		}
		if existsRegular(activePath) {
			return true, UpdateResult{}, false, nil
		}
		if time.Now().After(deadline) {
			rmErr := os.Remove(reqPath)
			switch {
			case rmErr == nil:
				return false, UpdateResult{}, false, nil
			case errors.Is(rmErr, os.ErrNotExist):
				// A running relay claimed the request right at the deadline.
				return true, UpdateResult{}, false, nil
			default:
				return false, UpdateResult{}, false, rmErr
			}
		}
		select {
		case <-ctx.Done():
			_ = os.Remove(reqPath)
			return false, UpdateResult{}, false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (u *Updater) waitForResult(ctx context.Context, id, activePath, resPath string) (UpdateResult, error) {
	poll := u.getPollInterval()
	staleAfter := u.getStaleTimeout()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	var (
		lastSeq      uint64
		seenSeq      bool
		lastProgress = time.Now()
	)
	for {
		if r, ok, err := readResponse(resPath, id); err != nil {
			return UpdateResult{ViaRelay: true}, err
		} else if ok {
			return r.result(), r.err()
		}

		marker, exists, err := readActiveMarker(activePath, id)
		if err != nil {
			return UpdateResult{ViaRelay: true}, err
		}
		if !exists {
			// Check resPath once more because the relay writes resPath before removing activePath.
			if r, ok, err := readResponse(resPath, id); err != nil {
				return UpdateResult{ViaRelay: true}, err
			} else if ok {
				return r.result(), r.err()
			}
			return UpdateResult{ViaRelay: true}, errors.New("the running relay stopped before finishing the web client update")
		}
		if !seenSeq || marker.Seq != lastSeq {
			seenSeq = true
			lastSeq = marker.Seq
			lastProgress = time.Now()
		} else if time.Since(lastProgress) > staleAfter {
			return UpdateResult{ViaRelay: true}, errors.New("the running relay stopped before finishing the web client update")
		}

		select {
		case <-ctx.Done():
			return UpdateResult{ViaRelay: true}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (u *Updater) runOfflineUpdate(ctx context.Context, controlDir, id string) (UpdateResult, error) {
	lockPath := filepath.Join(controlDir, offlineLock)
	releaseLock, err := u.acquireOfflineLock(ctx, lockPath, id)
	if err != nil {
		return UpdateResult{}, err
	}
	defer releaseLock()

	u.Load()
	res, err := u.UpdateDetail(ctx)
	cleanOrphanControlFiles(controlDir, u.getOrphanMaxAge())
	return res, err
}

// acquireOfflineLock takes an exclusive file lock in controlDir so two offline CLI runs never
// unpack and prune against each other. A stale lock whose heartbeat stopped is recovered safely.
func (u *Updater) acquireOfflineLock(ctx context.Context, lockPath, id string) (func(), error) {
	poll := u.getPollInterval()
	staleAfter := u.getStaleTimeout()

	var (
		observedID   string
		observedSeq  uint64
		lastProgress = time.Now()
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ok, err := createOfflineLock(lockPath, id)
		if err != nil {
			return nil, err
		}
		if ok {
			return u.startOfflineLockHeartbeat(lockPath, id), nil
		}

		marker, exists, err := readAnyActiveMarker(lockPath)
		if err != nil {
			// A corrupted lock file that is not being refreshed is treated as stale after staleAfter.
			if time.Since(lastProgress) > staleAfter {
				removeStaleLock(lockPath)
				lastProgress = time.Now()
			}
		} else if !exists {
			continue
		} else if marker.ID != observedID || marker.Seq != observedSeq {
			observedID = marker.ID
			observedSeq = marker.Seq
			lastProgress = time.Now()
		} else if time.Since(lastProgress) > staleAfter {
			removeStaleLock(lockPath)
			observedID = ""
			observedSeq = 0
			lastProgress = time.Now()
			continue
		}

		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func createOfflineLock(lockPath, id string) (bool, error) {
	f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(activeMarker{ID: id, Seq: 1})
	if err != nil {
		f.Close()
		os.Remove(lockPath)
		return false, err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil {
		os.Remove(lockPath)
		return false, werr
	}
	if cerr != nil {
		os.Remove(lockPath)
		return false, cerr
	}
	return true, nil
}

func (u *Updater) startOfflineLockHeartbeat(lockPath, id string) func() {
	hbCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	interval := u.getHeartbeatInterval()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var seq uint64 = 1
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				seq++
				if b, err := json.Marshal(activeMarker{ID: id, Seq: seq}); err == nil {
					_ = writeAtomicFile(lockPath, ".lock-*", b)
				}
			}
		}
	}()

	return func() {
		cancel()
		<-done
		if marker, exists, err := readAnyActiveMarker(lockPath); err == nil && exists && marker.ID == id {
			_ = os.Remove(lockPath)
		}
	}
}

func removeStaleLock(lockPath string) {
	stalePath := lockPath + ".stale-" + randomSuffix()
	if err := os.Rename(lockPath, stalePath); err == nil {
		_ = os.Remove(stalePath)
	}
}

// watchTriggers polls u.Dir/.control for manual update requests from `kettu-relay web update`.
// When idle it performs only read-only checks with zero disk writes.
func (u *Updater) watchTriggers(ctx context.Context, logf func(string, ...any)) {
	if strings.TrimSpace(u.Dir) == "" {
		return
	}
	controlDir := filepath.Join(u.Dir, controlDirName)
	poll := u.getPollInterval()
	hbInterval := u.getHeartbeatInterval()
	orphanAge := u.getOrphanMaxAge()

	var (
		activeMu  sync.Mutex
		activeSeq = make(map[string]uint64)
		pending   []string
		notify    = make(chan struct{}, 1)
	)

	finishBatch := func(ids []string, res UpdateResult, err error, stopping bool) {
		for _, id := range ids {
			resp := triggerResponse{
				ID:      id,
				Updated: res.Updated,
				From:    res.From,
				To:      res.To,
			}
			if stopping {
				resp.Updated = false
				resp.Error = "relay is stopping"
			} else if err != nil {
				resp.Error = err.Error()
			}
			resPath := filepath.Join(controlDir, resPrefix+id+jsonSuffix)
			if b, merr := json.Marshal(resp); merr == nil {
				_ = writeAtomicFile(resPath, ".res-*", b)
			}

			activeMu.Lock()
			delete(activeSeq, id)
			_ = os.Remove(filepath.Join(controlDir, activePrefix+id+jsonSuffix))
			activeMu.Unlock()
		}
	}

	var workerWg sync.WaitGroup
	workerWg.Add(1)
	go func() {
		defer workerWg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-notify:
			}
			for {
				activeMu.Lock()
				ids := pending
				pending = nil
				activeMu.Unlock()
				if len(ids) == 0 {
					break
				}
				if ctx.Err() != nil {
					_, tag := u.Current()
					finishBatch(ids, UpdateResult{From: tag, To: tag}, nil, true)
					return
				}
				res, err := u.UpdateDetail(ctx)
				if ctx.Err() != nil {
					finishBatch(ids, res, err, true)
					return
				}
				u.logResult(logf, res, err, true)
				finishBatch(ids, res, err, false)
			}
		}
	}()

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	lastHeartbeat := time.Now()
	lastCleanup := time.Now()
	cleanupInterval := min(time.Minute, orphanAge)

	for {
		// Claim any new request files in controlDir.
		if info, err := os.Lstat(controlDir); err == nil && info.IsDir() {
			if entries, err := os.ReadDir(controlDir); err == nil && len(entries) > 0 {
				claimedAny := false
				for _, e := range entries {
					if e.IsDir() {
						continue
					}
					id, ok := parsePrefixedID(e.Name(), reqPrefix)
					if !ok {
						continue
					}
					reqPath := filepath.Join(controlDir, e.Name())
					if !validRequestFile(reqPath, id) {
						continue
					}
					activePath := filepath.Join(controlDir, activePrefix+id+jsonSuffix)
					activeMu.Lock()
					renameErr := os.Rename(reqPath, activePath)
					if renameErr == nil {
						activeSeq[id] = 0
						pending = append(pending, id)
						claimedAny = true
					}
					activeMu.Unlock()
				}
				if claimedAny {
					select {
					case notify <- struct{}{}:
					default:
					}
				}
				if time.Since(lastCleanup) >= cleanupInterval {
					cleanOrphanControlFiles(controlDir, orphanAge)
					lastCleanup = time.Now()
				}
			}
		}

		// Refresh active markers only while at least one manual request is in flight.
		if time.Since(lastHeartbeat) >= hbInterval {
			activeMu.Lock()
			if len(activeSeq) > 0 {
				for id, seq := range activeSeq {
					seq++
					activeSeq[id] = seq
					activePath := filepath.Join(controlDir, activePrefix+id+jsonSuffix)
					if b, err := json.Marshal(activeMarker{ID: id, Seq: seq}); err == nil {
						_ = writeAtomicFile(activePath, ".active-*", b)
					}
				}
			}
			activeMu.Unlock()
			lastHeartbeat = time.Now()
		}

		select {
		case <-ctx.Done():
			workerWg.Wait()
			// Complete any requests claimed right as ctx was cancelled.
			activeMu.Lock()
			var remaining []string
			for id := range activeSeq {
				remaining = append(remaining, id)
			}
			pending = nil
			activeMu.Unlock()
			if len(remaining) > 0 {
				_, tag := u.Current()
				finishBatch(remaining, UpdateResult{From: tag, To: tag}, nil, true)
			}
			return
		case <-ticker.C:
		}
	}
}

func (r triggerResponse) result() UpdateResult {
	return UpdateResult{
		Updated:  r.Updated,
		From:     r.From,
		To:       r.To,
		ViaRelay: true,
	}
}

func (r triggerResponse) err() error {
	if r.Error == "" {
		return nil
	}
	if r.Error == ErrNoClient.Error() {
		return ErrNoClient
	}
	return errors.New(r.Error)
}

func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func validRequestID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func parsePrefixedID(name, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, jsonSuffix)
	if !ok || !validRequestID(id) {
		return "", false
	}
	return id, true
}

func existsRegular(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func readLimitedPrivate(path string) ([]byte, error) {
	if err := private.File(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a plain file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxControlFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxControlFileSize {
		return nil, fmt.Errorf("%s is too large", path)
	}
	return b, nil
}

func validRequestFile(path, wantID string) bool {
	b, err := readLimitedPrivate(path)
	if err != nil {
		return false
	}
	var req triggerRequest
	return json.Unmarshal(b, &req) == nil && req.ID == wantID
}

func readResponse(path, wantID string) (triggerResponse, bool, error) {
	b, err := readLimitedPrivate(path)
	if errors.Is(err, os.ErrNotExist) {
		return triggerResponse{}, false, nil
	}
	if err != nil {
		return triggerResponse{}, false, err
	}
	var resp triggerResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		return triggerResponse{}, false, fmt.Errorf("invalid update response: %w", err)
	}
	if resp.ID != wantID {
		return triggerResponse{}, false, errors.New("update response does not match request")
	}
	_ = os.Remove(path)
	return resp, true, nil
}

func readActiveMarker(path, wantID string) (activeMarker, bool, error) {
	b, err := readLimitedPrivate(path)
	if errors.Is(err, os.ErrNotExist) {
		return activeMarker{}, false, nil
	}
	if err != nil {
		return activeMarker{}, false, err
	}
	var m activeMarker
	if err := json.Unmarshal(b, &m); err != nil {
		return activeMarker{}, false, err
	}
	if m.ID != wantID {
		return activeMarker{}, false, errors.New("active update marker does not match request")
	}
	return m, true, nil
}

func readAnyActiveMarker(path string) (activeMarker, bool, error) {
	b, err := readLimitedPrivate(path)
	if errors.Is(err, os.ErrNotExist) {
		return activeMarker{}, false, nil
	}
	if err != nil {
		return activeMarker{}, false, err
	}
	var m activeMarker
	if err := json.Unmarshal(b, &m); err != nil {
		return activeMarker{}, false, err
	}
	if !validRequestID(m.ID) {
		return activeMarker{}, false, errors.New("invalid lock marker")
	}
	return m, true, nil
}

func cleanOrphanControlFiles(controlDir string, maxAge time.Duration) {
	entries, err := os.ReadDir(controlDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, reqPrefix) &&
			!strings.HasPrefix(name, activePrefix) &&
			!strings.HasPrefix(name, resPrefix) &&
			!strings.HasPrefix(name, ".req-") &&
			!strings.HasPrefix(name, ".active-") &&
			!strings.HasPrefix(name, ".res-") &&
			!strings.HasPrefix(name, ".lock-") &&
			!strings.HasPrefix(name, offlineLock+".stale-") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(controlDir, name))
	}
}
