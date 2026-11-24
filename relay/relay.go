package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ErrUnauthorized is what an Admission returns for a host credential it does not take; the host
// is closed with 4401.
var ErrUnauthorized = errors.New("relay: credential not accepted")

// ErrForbidden is what an Admission returns for a device it does not let reach the host; the
// device is closed with 4403.
var ErrForbidden = errors.New("relay: device not allowed")

// Grant is what an Admission gave a signed-in host. The relay keeps it with the host and with
// each device that reached the host through it.
type Grant interface {
	// Deadline is when the grant lapses and the host is closed with 4401; zero for never.
	Deadline() time.Time
}

// Admission decides who may use the relay. Any other error than ErrUnauthorized and ErrForbidden
// closes the socket with 1011, so the client tries again.
type Admission interface {
	// Host checks the credential a host signs in with, the sign-in's token, after its signature
	// proved it holds the id's key.
	Host(ctx context.Context, id, credential string) (Grant, error)
	// Renew checks the credential of a signed-in host's token message against its grant.
	Renew(ctx context.Context, id string, current Grant, credential string) (Grant, error)
	// Device checks a device's connect against the grant of the host it asks for.
	Device(ctx context.Context, id string, connect DeviceMessage, host Grant) error
}

// Options configure a Relay.
type Options struct {
	Admission Admission
	// Origin is the relay origin hosts sign, for a request; it must be the origin clients use.
	Origin func(*http.Request) string
	// Address is the client address a request counts against for rate limits.
	Address func(*http.Request) string
	// Features are what GET /v1 lists; the relay itself serves FeatureDrain.
	Features func() []string
	// Version is the program's version, which GET /v1 names.
	Version string
	// ConnectionsPerMinute caps new sockets per address; DevicesPerMinute caps device sockets per
	// host id. Zero takes the defaults.
	ConnectionsPerMinute int
	DevicesPerMinute     int
}

// Default rate limits.
const (
	DefaultConnectionsPerMinute = 120
	DefaultDevicesPerMinute     = 60
)

const (
	keepaliveInterval = 30 * time.Second
	writeTimeout      = 15 * time.Second
	admissionTimeout  = 10 * time.Second
	queueLength       = 64
)

type kind int

const (
	kindHost kind = iota
	kindDevice
	kindAccept
)

type state int

const (
	challenged state = iota
	verifying
	ready
	opened
	waiting
	joined
)

type frame struct {
	typ  websocket.MessageType
	data []byte
}

type socket struct {
	conn *websocket.Conn
	kind kind
	room *room
	out  chan frame
	dead chan struct{}
	once sync.Once
	code int

	// Guarded by Relay.mu.
	state    state
	ended    bool
	gen      int
	timer    *time.Timer
	nonce    string
	origin   string
	grant    Grant
	features []string
	token    string
	via      *socket
	pair     *socket
}

// room is one host id's sockets.
type room struct {
	id      string
	host    *socket
	hosts   map[*socket]struct{}
	devices map[*socket]struct{}
	waiting map[string]*socket
	accepts int
}

// Relay serves protocol 1. Its zero value is not usable; make one with New.
type Relay struct {
	opts    Options
	limiter *limiter

	mu       sync.Mutex
	rooms    map[string]*room
	draining bool
	stopped  bool
	active   sync.WaitGroup
}

// New makes a relay from its options; Admission, Origin and Address are required.
func New(opts Options) (*Relay, error) {
	if opts.Admission == nil || opts.Origin == nil || opts.Address == nil {
		return nil, errors.New("relay: Admission, Origin and Address are required")
	}
	if opts.Features == nil {
		opts.Features = func() []string { return []string{FeatureDrain} }
	}
	if opts.ConnectionsPerMinute <= 0 {
		opts.ConnectionsPerMinute = DefaultConnectionsPerMinute
	}
	if opts.DevicesPerMinute <= 0 {
		opts.DevicesPerMinute = DefaultDevicesPerMinute
	}
	return &Relay{opts: opts, limiter: newLimiter(), rooms: map[string]*room{}}, nil
}

// Handles reports whether a path is the relay's, so a program can serve other paths beside it.
func (r *Relay) Handles(path string) bool {
	return path == "/v1" || strings.HasPrefix(path, "/v1/")
}

// ServeHTTP answers GET /v1, /v1/ping and the socket paths; everything else under /v1 is 404.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case "/v1":
		r.serveInfo(w, req)
		return
	case "/v1/ping":
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	path, ok := ParseSocketPath(req.URL.Path)
	if !ok {
		http.NotFound(w, req)
		return
	}
	if req.Method != http.MethodGet || !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		w.WriteHeader(http.StatusUpgradeRequired)
		return
	}
	refusal := 0
	address := r.opts.Address(req)
	switch {
	case !r.limiter.take(address, r.opts.ConnectionsPerMinute):
		refusal = CloseTooMany
	case path.Kind == "connect" && !r.limiter.take("device:"+path.ID, r.opts.DevicesPerMinute):
		refusal = CloseTooMany
	case path.Kind == "accept" && !IsToken(path.Token):
		refusal = CloseOffline
	}
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		// Nothing here rests on cookies or a page's origin: hosts prove their key and devices
		// are checked by the host inside the channel, so any origin may connect.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	if refusal != 0 {
		// Accepted only to be closed with the code, the one refusal a browser can read.
		conn.Close(websocket.StatusCode(refusal), "")
		return
	}
	r.serveSocket(conn, path, r.opts.Origin(req))
}

func (r *Relay) serveInfo(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	features := r.opts.Features()
	if features == nil {
		features = []string{}
	}
	body, _ := json.Marshal(Info{
		Relay:    "kettu",
		Protocol: Protocol,
		Regions:  []json.RawMessage{},
		Features: features,
		Version:  r.opts.Version,
	})
	w.Header().Set("Content-Type", "application/json")
	if req.Method == http.MethodHead {
		return
	}
	w.Write(body)
}

func (r *Relay) serveSocket(conn *websocket.Conn, path SocketPath, origin string) {
	conn.SetReadLimit(MaxMessage)
	s := &socket{conn: conn, out: make(chan frame, queueLength), dead: make(chan struct{})}

	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		conn.Close(CloseGoingAway, "")
		return
	}
	r.active.Add(1)
	defer r.active.Done()
	rm := r.rooms[path.ID]
	if rm == nil {
		rm = &room{id: path.ID, hosts: map[*socket]struct{}{}, devices: map[*socket]struct{}{}, waiting: map[string]*socket{}}
		r.rooms[path.ID] = rm
	}
	s.room = rm
	refusal := 0
	switch path.Kind {
	case "host":
		s.kind, s.state, s.origin, s.nonce = kindHost, challenged, origin, NewToken()
		rm.hosts[s] = struct{}{}
		r.deadline(s, SignInTimeout, CloseTimeout)
		s.trySend(websocket.MessageText, challengeMessage(s.nonce))
	case "connect":
		if len(rm.devices) >= MaxDevices {
			refusal = CloseTooMany
			break
		}
		s.kind, s.state = kindDevice, opened
		rm.devices[s] = struct{}{}
		r.deadline(s, ConnectTimeout, CloseTimeout)
	case "accept":
		device := rm.waiting[path.Token]
		if device == nil || device.ended {
			refusal = CloseOffline
			break
		}
		delete(rm.waiting, path.Token)
		s.kind, s.state, s.pair = kindAccept, joined, device
		rm.accepts++
		device.state, device.pair = joined, s
		r.stopTimer(device)
		device.trySend(websocket.MessageText, msgJoined)
		if r.draining && hasDrain(device) {
			device.trySend(websocket.MessageText, msgDrain)
		}
	}
	if refusal != 0 {
		r.tidy(rm)
		r.mu.Unlock()
		conn.Close(websocket.StatusCode(refusal), "")
		return
	}
	r.mu.Unlock()

	go s.write()
	go s.keepalive()
	r.read(s)
}

func hasDrain(s *socket) bool {
	for _, f := range s.features {
		if f == FeatureDrain {
			return true
		}
	}
	return false
}

func (r *Relay) read(s *socket) {
	ctx := context.Background()
	for {
		typ, data, err := s.conn.Read(ctx)
		if err != nil {
			r.mu.Lock()
			if errors.Is(err, websocket.ErrMessageTooBig) {
				r.end(s, CloseTooBig, CloseTooBig)
			} else {
				r.end(s, CloseNormal, CloseNormal)
			}
			r.mu.Unlock()
			return
		}
		if typ == websocket.MessageText && string(data) == Ping {
			s.trySend(websocket.MessageText, []byte(Pong))
			continue
		}
		r.message(s, typ, data)
	}
}

func (r *Relay) message(s *socket, typ websocket.MessageType, data []byte) {
	r.mu.Lock()
	if s.ended {
		r.mu.Unlock()
		return
	}
	switch {
	case s.kind == kindAccept || (s.kind == kindDevice && s.state == joined):
		if typ != websocket.MessageBinary {
			r.end(s, CloseMalformed, CloseMalformed)
			r.mu.Unlock()
			return
		}
		pair := s.pair
		r.mu.Unlock()
		if pair == nil {
			r.mu.Lock()
			r.end(s, CloseNormal, CloseNormal)
			r.mu.Unlock()
			return
		}
		// Blocks while the pair's queue is full, which slows this socket's reads in turn.
		pair.send(frame{typ: websocket.MessageBinary, data: data})
	case s.kind == kindDevice:
		r.deviceMessage(s, typ, data)
	default:
		r.hostMessage(s, typ, data)
	}
}

// hostMessage runs with r.mu held and returns with it released.
func (r *Relay) hostMessage(s *socket, typ websocket.MessageType, data []byte) {
	defer r.mu.Unlock()
	var message HostMessage
	ok := typ == websocket.MessageText
	if ok {
		message, ok = ParseHostMessage(data)
	}
	switch {
	case ok && s.state == challenged && message.Type == "sign-in":
		s.state = verifying
		r.stopTimer(s)
		id, origin, nonce := s.room.id, s.origin, s.nonce
		r.mu.Unlock()
		if !VerifySignIn(message.PublicKey, message.Signature, origin, id, nonce) {
			r.mu.Lock()
			r.end(s, CloseUnauthorized, CloseUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), admissionTimeout)
		grant, err := r.opts.Admission.Host(ctx, id, message.Credential)
		cancel()
		r.mu.Lock()
		if s.ended || s.state != verifying {
			return
		}
		if err != nil {
			r.end(s, admissionClose(err), CloseNormal)
			return
		}
		rm := s.room
		if old := rm.host; old != nil && old != s {
			old.trySend(websocket.MessageText, msgReplaced)
			r.end(old, CloseReplaced, CloseReplaced)
		}
		rm.host, s.state, s.grant = s, ready, grant
		r.grantDeadline(s)
		s.trySend(websocket.MessageText, msgReady)
		if r.draining {
			s.trySend(websocket.MessageText, msgDrain)
		}
	case ok && s.state == ready && message.Type == "token":
		id, current := s.room.id, s.grant
		r.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), admissionTimeout)
		grant, err := r.opts.Admission.Renew(ctx, id, current, message.Credential)
		cancel()
		r.mu.Lock()
		if s.ended || s.state != ready {
			return
		}
		if err != nil {
			r.end(s, admissionClose(err), CloseNormal)
			return
		}
		s.grant = grant
		r.grantDeadline(s)
	case ok && s.state == ready && message.Type == "profile":
		// This relay lists no hosts; the profile is dropped.
	default:
		r.end(s, CloseMalformed, CloseMalformed)
	}
}

// deviceMessage runs with r.mu held and returns with it released.
func (r *Relay) deviceMessage(s *socket, typ websocket.MessageType, data []byte) {
	defer r.mu.Unlock()
	if s.state != opened || typ != websocket.MessageText || !IsConnect(data) {
		r.end(s, CloseMalformed, CloseMalformed)
		return
	}
	rm := s.room
	host := rm.host
	if host == nil {
		r.end(s, CloseOffline, CloseOffline)
		return
	}
	connect, ok := ParseDeviceMessage(data)
	if !ok {
		r.end(s, CloseForbidden, CloseForbidden)
		return
	}
	s.state = verifying
	r.stopTimer(s)
	grant := host.grant
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), admissionTimeout)
	err := r.opts.Admission.Device(ctx, rm.id, connect, grant)
	cancel()
	r.mu.Lock()
	if s.ended || s.state != verifying {
		return
	}
	if err != nil {
		r.end(s, admissionClose(err), CloseNormal)
		return
	}
	// The host may have gone or been replaced while the device was checked.
	if rm.host != host {
		r.end(s, CloseOffline, CloseOffline)
		return
	}
	token := NewToken()
	s.state, s.token, s.grant, s.via, s.features = waiting, token, grant, host, connect.Features
	rm.waiting[token] = s
	r.deadline(s, AcceptTimeout, CloseOffline)
	host.trySend(websocket.MessageText, incomingMessage(token))
}

func admissionClose(err error) int {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return CloseUnauthorized
	case errors.Is(err, ErrForbidden):
		return CloseForbidden
	}
	return CloseUnavailable
}

// end closes a socket with code and what hangs on it: its pair with pairCode, and a ready host's
// waiting devices as offline. Runs with r.mu held.
func (r *Relay) end(s *socket, code, pairCode int) {
	if s.ended {
		return
	}
	s.ended = true
	r.stopTimer(s)
	s.close(code)
	rm := s.room
	switch s.kind {
	case kindHost:
		delete(rm.hosts, s)
		if rm.host == s {
			rm.host = nil
			for _, device := range rm.waiting {
				if device.via == s {
					r.end(device, CloseOffline, CloseOffline)
				}
			}
		}
	case kindDevice:
		delete(rm.devices, s)
		if s.state == waiting {
			delete(rm.waiting, s.token)
		}
		if s.pair != nil {
			r.end(s.pair, pairCode, pairCode)
		}
	case kindAccept:
		rm.accepts--
		if s.pair != nil {
			r.end(s.pair, pairCode, pairCode)
		}
	}
	r.tidy(rm)
}

func (r *Relay) tidy(rm *room) {
	if rm.host == nil && len(rm.hosts) == 0 && len(rm.devices) == 0 && rm.accepts == 0 && r.rooms[rm.id] == rm {
		delete(r.rooms, rm.id)
	}
}

// deadline closes s with code after d unless its state changes first. Runs with r.mu held.
func (r *Relay) deadline(s *socket, d time.Duration, code int) {
	r.stopTimer(s)
	gen := s.gen
	s.timer = time.AfterFunc(d, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if s.gen == gen && !s.ended {
			r.end(s, code, code)
		}
	})
}

func (r *Relay) grantDeadline(s *socket) {
	r.stopTimer(s)
	if s.grant == nil {
		return
	}
	if at := s.grant.Deadline(); !at.IsZero() {
		r.deadline(s, time.Until(at), CloseUnauthorized)
	}
}

func (r *Relay) stopTimer(s *socket) {
	s.gen++
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// Revoke closes the signed-in hosts whose grant matches with 4401, and the devices that reached
// a host through such a grant with 4403.
func (r *Relay) Revoke(match func(Grant) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rm := range r.rooms {
		for s := range rm.devices {
			if (s.state == waiting || s.state == joined) && s.grant != nil && match(s.grant) {
				r.end(s, CloseForbidden, CloseNormal)
			}
		}
		if host := rm.host; host != nil && host.grant != nil && match(host.grant) {
			r.end(host, CloseUnauthorized, CloseUnauthorized)
		}
	}
}

// Drain tells every ready host, and every joined device that asked, to move elsewhere; hosts that
// sign in and devices that join from now on are told the same.
func (r *Relay) Drain() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.draining = true
	for _, rm := range r.rooms {
		if rm.host != nil {
			rm.host.trySend(websocket.MessageText, msgDrain)
		}
		for s := range rm.devices {
			if s.state == joined && hasDrain(s) {
				s.trySend(websocket.MessageText, msgDrain)
			}
		}
	}
}

// Shutdown drains the relay, waits for its sockets to close until ctx is done, then closes the
// rest with 1001 and takes no more.
func (r *Relay) Shutdown(ctx context.Context) {
	r.Drain()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for r.Sockets() > 0 {
		select {
		case <-ctx.Done():
			r.closeAll()
			return
		case <-ticker.C:
		}
	}
	r.closeAll()
}

func (r *Relay) closeAll() {
	r.mu.Lock()
	r.stopped = true
	for _, rm := range r.rooms {
		for s := range rm.hosts {
			r.end(s, CloseGoingAway, CloseGoingAway)
		}
		for s := range rm.devices {
			r.end(s, CloseGoingAway, CloseGoingAway)
		}
	}
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.active.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// Sockets is how many sockets the relay holds now.
func (r *Relay) Sockets() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rm := range r.rooms {
		n += len(rm.hosts) + len(rm.devices) + rm.accepts
	}
	return n
}

// close asks the writer to send what is queued and close with code.
func (s *socket) close(code int) {
	s.once.Do(func() {
		s.code = code
		close(s.dead)
	})
}

// trySend queues a message without waiting; a socket whose queue is full is stuck and is closed.
func (s *socket) trySend(typ websocket.MessageType, data []byte) {
	select {
	case <-s.dead:
	case s.out <- frame{typ: typ, data: data}:
	default:
		s.close(CloseGoingAway)
	}
}

func (s *socket) send(f frame) {
	select {
	case <-s.dead:
	case s.out <- f:
	}
}

func (s *socket) write() {
	for {
		select {
		case f := <-s.out:
			if !s.writeFrame(f) {
				return
			}
		case <-s.dead:
			for {
				select {
				case f := <-s.out:
					if !s.writeFrame(f) {
						return
					}
				default:
					s.conn.Close(websocket.StatusCode(s.code), "")
					return
				}
			}
		}
	}
}

func (s *socket) writeFrame(f frame) bool {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if s.conn.Write(ctx, f.typ, f.data) != nil {
		s.close(CloseGoingAway)
		s.conn.CloseNow()
		return false
	}
	return true
}

// keepalive pings the socket and drops it when a ping goes unanswered.
func (s *socket) keepalive() {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.dead:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), keepaliveInterval)
			err := s.conn.Ping(ctx)
			cancel()
			if err != nil {
				s.conn.CloseNow()
				return
			}
		}
	}
}
