package relay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"omenight/internal/admin"
	"omenight/internal/api"
	"omenight/internal/identity"
	"omenight/internal/proto"
	"omenight/internal/store"
	"omenight/internal/xio"
)

const defaultHeartbeat = 15 // seconds

type Config struct {
	Addr     string
	HostKey  string
	State    string
	Socket   string
	PortBase int
}

type Relay struct {
	cfg     Config
	signer  ssh.Signer
	store   *store.Store
	admin   *admin.Admin
	started time.Time

	mu       sync.Mutex
	sessions map[string]*session
}

func New(cfg Config) (*Relay, error) {
	signer, err := identity.LoadOrCreate(cfg.HostKey)
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	st, err := store.Open(cfg.State)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	return &Relay{
		cfg:      cfg,
		signer:   signer,
		store:    st,
		admin:    admin.New(st, cfg.PortBase),
		started:  time.Now(),
		sessions: map[string]*session{},
	}, nil
}

func (r *Relay) HostKeyFingerprint() string {
	return identity.Fingerprint(r.signer.PublicKey())
}

func (r *Relay) Store() *store.Store { return r.store }

func (r *Relay) Serve() error {
	ln, err := net.Listen("tcp", r.cfg.Addr)
	if err != nil {
		return err
	}
	log.Printf("relay listening on %s (host key %s)", ln.Addr(), r.HostKeyFingerprint())

	go r.watch()

	if r.cfg.Socket != "" {
		go r.serveSocket(r.cfg.Socket)
	}

	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go r.handle(c)
	}
}

// Online returns the names of currently connected clients.
func (r *Relay) Online() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.sessions))
	for name := range r.sessions {
		out = append(out, name)
	}
	return out
}

func (r *Relay) snapshot() []*session {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out
}

// watch makes CLI edits take effect on live sessions: the daemon reloads the
// store and drops any session whose grants no longer match policy, so the
// agent reconnects and renegotiates.
func (r *Relay) watch() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		changed, err := r.store.Reload()
		if err != nil {
			log.Printf("reload: %v", err)
			continue
		}
		if !changed {
			continue
		}
		r.revalidateAll()
	}
}

// revalidateAll applies policy changes to live sessions. Called both after an
// external store edit and after a command served by this daemon (which the
// file watcher cannot see, because the daemon updated the mtime itself).
func (r *Relay) revalidateAll() {
	for _, s := range r.snapshot() {
		s.revalidate()
	}
}

func (r *Relay) auth(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	if _, err := r.store.Reload(); err != nil {
		return nil, err
	}
	fp := identity.Fingerprint(key)
	cl, err := r.store.Register(c.User(), fp)
	if err != nil {
		return nil, err
	}
	if cl.Status == store.StatusRevoked {
		log.Printf("rejecting revoked key %s (%s)", cl.Name, fp)
		return nil, fmt.Errorf("key revoked")
	}
	return &ssh.Permissions{Extensions: map[string]string{
		"fp":     fp,
		"name":   cl.Name,
		"status": cl.Status,
	}}, nil
}

func (r *Relay) handle(tc net.Conn) {
	cfg := &ssh.ServerConfig{
		ServerVersion:     "SSH-2.0-omenight_" + proto.Version,
		PublicKeyCallback: r.auth,
	}
	cfg.AddHostKey(r.signer)

	sc, chans, reqs, err := ssh.NewServerConn(tc, cfg)
	if err != nil {
		log.Printf("handshake from %s: %v", tc.RemoteAddr(), err)
		return
	}

	s := &session{
		r:        r,
		conn:     sc,
		name:     sc.Permissions.Extensions["name"],
		fp:       sc.Permissions.Extensions["fp"],
		status:   sc.Permissions.Extensions["status"],
		started:  time.Now(),
		lastSeen: time.Now(),
		forwards: map[string]net.Listener{},
		done:     make(chan struct{}),
	}
	if !r.addSession(s) {
		return
	}
	defer r.dropSession(s)

	go s.handleRequests(reqs)
	go s.watchdog()

	for nc := range chans {
		if nc.ChannelType() != proto.ControlChannel {
			nc.Reject(ssh.UnknownChannelType, "unsupported channel")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(chReqs)
		go s.handleControl(ch)
	}
}

// Snapshot is the live view the TUI and CLI read.
func (r *Relay) Snapshot() api.Snapshot {
	snap := api.Snapshot{
		Listen:    r.cfg.Addr,
		Version:   proto.Version,
		HostKey:   r.HostKeyFingerprint(),
		StartedAt: r.started,
	}
	live := map[string]*session{}
	for _, s := range r.snapshot() {
		live[s.name] = s
	}
	for _, c := range r.store.All() {
		info := api.Client{
			Name:     c.Name,
			Status:   c.Status,
			Allow:    c.Allow,
			LastSeen: c.LastSeen,
		}
		if s, ok := live[c.Name]; ok {
			info.Online = true
			info.Remote = s.conn.RemoteAddr().String()
			info.Since = s.started
			info.Conns = s.conns.Load()
			info.Up = s.up.Load()
			info.Down = s.down.Load()
		}
		snap.Clients = append(snap.Clients, info)
	}
	return snap
}

// serveSocket publishes snapshots on a unix socket. One request, one
// document, then close: no protocol to get wrong.
func (r *Relay) serveSocket(path string) {
	os.Remove(path)
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		log.Printf("api: %v", err)
		return
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		log.Printf("api: %v (TUI disabled)", err)
		return
	}
	os.Chmod(path, 0o600)
	log.Printf("api on %s", path)

	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()

			// Three clients share this socket: a bare snapshot reader, a
			// command sender, and `curl --unix-socket` for debugging.
			br := bufio.NewReader(c)
			c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			line, _ := br.ReadString('\n')
			c.SetReadDeadline(time.Time{})
			c.SetWriteDeadline(time.Now().Add(5 * time.Second))

			if strings.HasPrefix(line, "GET ") {
				fmt.Fprint(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\n\r\n")
				json.NewEncoder(c).Encode(r.Snapshot())
				return
			}
			if strings.HasPrefix(strings.TrimSpace(line), "{") {
				var cmd api.Command
				if err := json.Unmarshal([]byte(line), &cmd); err == nil && cmd.Cmd != "" {
					json.NewEncoder(c).Encode(r.runCommand(cmd))
					return
				}
			}
			if err := json.NewEncoder(c).Encode(r.Snapshot()); err != nil {
				return
			}
		}(c)
	}
}

// runCommand is the write half of the socket. It mutates the store through
// the same admin code the CLI uses, then pushes the change into live sessions.
func (r *Relay) runCommand(cmd api.Command) api.Response {
	var (
		cl  store.Client
		err error
		msg string
	)
	switch cmd.Cmd {
	case "approve":
		if cl, err = r.admin.Approve(cmd.Name); err == nil {
			msg = fmt.Sprintf("%s approved, may bind %v", cl.Name, cl.Allow)
		}
	case "revoke":
		if cl, err = r.admin.Revoke(cmd.Name); err == nil {
			msg = fmt.Sprintf("%s revoked", cl.Name)
		}
	case "allow":
		if cl, err = r.admin.Allow(cmd.Name, cmd.Addr); err == nil {
			msg = fmt.Sprintf("%s may bind %v", cl.Name, cl.Allow)
		}
	default:
		err = fmt.Errorf("unknown command %q", cmd.Cmd)
	}

	if err != nil {
		log.Printf("command %s %s: %v", cmd.Cmd, cmd.Name, err)
		return api.Response{OK: false, Message: err.Error(), Snapshot: ptr(r.Snapshot())}
	}

	log.Printf("%s", msg)
	r.revalidateAll()
	return api.Response{OK: true, Message: msg, Snapshot: ptr(r.Snapshot())}
}

func ptr(s api.Snapshot) *api.Snapshot { return &s }

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

func (r *Relay) addSession(s *session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.sessions[s.name]; ok {
		log.Printf("%s: duplicate session from %s, rejecting (already %s)",
			s.name, s.conn.RemoteAddr(), old.conn.RemoteAddr())
		s.conn.Close()
		return false
	}
	r.sessions[s.name] = s
	log.Printf("%s connected (%s, %s)", s.name, s.status, s.conn.RemoteAddr())
	return true
}

func (r *Relay) dropSession(s *session) {
	s.closeAll()
	r.mu.Lock()
	if cur, ok := r.sessions[s.name]; ok && cur == s {
		delete(r.sessions, s.name)
	}
	r.mu.Unlock()
	r.store.Touch(s.fp)
	r.store.Flush()
	log.Printf("%s disconnected", s.name)
}

type session struct {
	r       *Relay
	conn    *ssh.ServerConn
	name    string
	fp      string
	status  string
	started time.Time

	conns atomic.Int64
	up    atomic.Int64
	down  atomic.Int64

	mu       sync.Mutex
	granted  []string
	forwards map[string]net.Listener
	lastSeen time.Time

	done     chan struct{}
	doneOnce sync.Once
}

func (s *session) closeAll() {
	s.doneOnce.Do(func() { close(s.done) })
	s.mu.Lock()
	for addr, ln := range s.forwards {
		ln.Close()
		delete(s.forwards, addr)
		log.Printf("%s: unbind %s", s.name, addr)
	}
	s.mu.Unlock()
	s.conn.Close()
}

func (s *session) isGranted(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.granted {
		if a == addr {
			return true
		}
	}
	return false
}

// handleControl runs the hello/config handshake, then consumes heartbeats.
func (s *session) handleControl(ch ssh.Channel) {
	defer ch.Close()
	dec := proto.NewMsgReader(ch)

	msg, err := dec.Next()
	if err != nil || msg.Hello == nil {
		log.Printf("%s: bad hello: %v", s.name, err)
		return
	}

	granted, reason := s.grant(msg.Hello.Tunnels)
	s.mu.Lock()
	s.granted = granted
	s.mu.Unlock()

	log.Printf("%s: wants %v, granted %v", s.name, msg.Hello.Tunnels, granted)
	if err := proto.WriteMsg(ch, proto.Msg{Type: proto.TypeConfig, Config: &proto.Config{
		Granted:      granted,
		HeartbeatSec: defaultHeartbeat,
		Reason:       reason,
	}}); err != nil {
		return
	}

	for {
		if _, err := dec.Next(); err != nil {
			return
		}
		s.mu.Lock()
		s.lastSeen = time.Now()
		s.mu.Unlock()
		s.r.store.Touch(s.fp)
	}
}

// grant intersects what the agent wants with what the admin allowed.
func (s *session) grant(want []string) ([]string, string) {
	if s.status != store.StatusActive {
		return nil, "awaiting approval"
	}
	cl, ok := s.r.store.ByFingerprint(s.fp)
	if !ok {
		return nil, "unknown client"
	}
	// An empty request means "whatever I was allowed": that is how egress
	// nodes work, because the relay owns the port number and the agent has
	// no opinion about it.
	if len(want) == 0 {
		if len(cl.Allow) == 0 {
			return nil, "no allowed tunnels"
		}
		return append([]string(nil), cl.Allow...), ""
	}

	var out []string
	for _, w := range want {
		for _, a := range cl.Allow {
			if a == w {
				out = append(out, w)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, "no allowed tunnels"
	}
	return out, ""
}

func (s *session) handleRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			s.handleForward(req)
		case "cancel-tcpip-forward":
			s.handleCancel(req)
		default:
			// keepalive@openssh.com and friends
			req.Reply(true, nil)
		}
	}
}

func (s *session) handleForward(req *ssh.Request) {
	var p proto.TCPIPForwardRequest
	if err := ssh.Unmarshal(req.Payload, &p); err != nil {
		req.Reply(false, nil)
		return
	}
	addr := net.JoinHostPort(p.BindAddr, strconv.Itoa(int(p.BindPort)))

	if !s.isGranted(addr) {
		log.Printf("%s: refused forward %s (not granted)", s.name, addr)
		req.Reply(false, nil)
		return
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("%s: bind %s: %v", s.name, addr, err)
		req.Reply(false, nil)
		return
	}
	port := ln.Addr().(*net.TCPAddr).Port

	s.mu.Lock()
	s.forwards[addr] = ln
	s.mu.Unlock()

	req.Reply(true, ssh.Marshal(proto.TCPIPForwardReply{BindPort: uint32(port)}))
	log.Printf("%s: bound %s", s.name, ln.Addr())
	go s.acceptLoop(ln, p.BindAddr, port)
}

func (s *session) handleCancel(req *ssh.Request) {
	var p proto.TCPIPForwardRequest
	if err := ssh.Unmarshal(req.Payload, &p); err != nil {
		req.Reply(false, nil)
		return
	}
	addr := net.JoinHostPort(p.BindAddr, strconv.Itoa(int(p.BindPort)))
	s.mu.Lock()
	if ln, ok := s.forwards[addr]; ok {
		ln.Close()
		delete(s.forwards, addr)
	}
	s.mu.Unlock()
	req.Reply(true, nil)
}

func (s *session) acceptLoop(ln net.Listener, bindAddr string, port int) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go s.pipe(c, bindAddr, port)
	}
}

func (s *session) pipe(c net.Conn, bindAddr string, port int) {
	defer c.Close()

	originIP := "0.0.0.0"
	originPort := 0
	if ta, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		originIP = ta.IP.String()
		originPort = ta.Port
	}

	payload := ssh.Marshal(proto.ForwardedTCPIPPayload{
		Addr:       bindAddr,
		Port:       uint32(port),
		OriginAddr: originIP,
		OriginPort: uint32(originPort),
	})

	ch, chReqs, err := s.conn.OpenChannel("forwarded-tcpip", payload)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(chReqs)

	s.conns.Add(1)
	defer s.conns.Add(-1)

	up, down := xio.Splice(c, ch)
	s.up.Add(up)
	s.down.Add(down)
	ch.Close()
}

func (s *session) revalidate() {
	cl, ok := s.r.store.ByFingerprint(s.fp)
	if !ok {
		s.closeAll()
		return
	}
	s.mu.Lock()
	granted := append([]string(nil), s.granted...)
	active := s.status
	s.mu.Unlock()

	if cl.Status != store.StatusActive {
		log.Printf("%s: %s, dropping session", s.name, cl.Status)
		s.closeAll()
		return
	}
	// Approved after connecting: reconnect to pick up the new grants.
	if active != store.StatusActive && len(granted) == 0 {
		log.Printf("%s: approved, reconnecting to apply grants", s.name)
		s.closeAll()
		return
	}
	for _, g := range granted {
		allowed := false
		for _, a := range cl.Allow {
			if a == g {
				allowed = true
				break
			}
		}
		if !allowed {
			log.Printf("%s: %s no longer allowed, dropping", s.name, g)
			s.closeAll()
			return
		}
	}
}

// watchdog is the half-open fix: a silent peer must not leave the relay
// holding a bound port with traffic hanging on it.
func (s *session) watchdog() {
	interval := time.Duration(defaultHeartbeat) * time.Second
	for {
		select {
		case <-s.done:
			return
		case <-time.After(interval / 3):
			s.mu.Lock()
			idle := time.Since(s.lastSeen)
			s.mu.Unlock()
			if idle > 2*interval {
				log.Printf("%s: heartbeat timeout after %s, dropping", s.name, idle.Round(time.Second))
				s.closeAll()
				return
			}
		}
	}
}
