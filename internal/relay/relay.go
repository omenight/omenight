package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"omenight/internal/admin"
	"omenight/internal/api"
	"omenight/internal/store"
	"omenight/internal/wg"
)

const Version = "0.4.2"

// The node's keepalive makes WireGuard rekey every two minutes, so a
// handshake older than this means the node is gone.
const onlineWindow = 3 * time.Minute

type Config struct {
	State  string
	Socket string
	Runner wg.Runner
}

type Relay struct {
	cfg     Config
	store   *store.Store
	admin   *admin.Admin
	ipv6    bool
	started time.Time

	syncMu sync.Mutex

	mu      sync.Mutex
	peers   map[string]wg.Peer // by interface
	syncErr string
}

func New(cfg Config) (*Relay, error) {
	st, err := store.Open(cfg.State)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	if _, err := st.Settings(); err != nil {
		return nil, err
	}
	if cfg.Runner == nil {
		cfg.Runner = wg.Exec{}
	}
	_, err = os.Stat("/proc/net/if_inet6")
	return &Relay{
		cfg:     cfg,
		store:   st,
		admin:   admin.New(st),
		ipv6:    err == nil,
		started: time.Now(),
		peers:   map[string]wg.Peer{},
	}, nil
}

// Serve keeps the kernel in line with the state file until ctx ends. Links
// stay up on exit, so restarting the daemon never drops traffic.
func (r *Relay) Serve(ctx context.Context) error {
	set, _ := r.store.Settings()
	pub, _ := wg.PublicKey(set.PrivateKey)
	log.Printf("relay %s, %s, public key %s", set.Endpoint, set.Kind, pub)

	r.sync()
	r.refresh()
	if r.cfg.Socket != "" {
		go r.serveSocket(r.cfg.Socket)
	}

	// The file watch makes CLI edits apply within 2s. The full resync every
	// 30s heals anything changed behind our back, like a deleted link.
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		changed, err := r.store.Reload()
		if err != nil {
			log.Printf("reload: %v", err)
		}
		if changed || n%15 == 0 {
			r.sync()
		}
		r.refresh()
	}
}

func (r *Relay) sync() {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()

	set, err := r.store.Settings()
	if err == nil {
		err = admin.Manager(set, r.cfg.Runner, r.ipv6).Sync(admin.Links(set, r.store.All()))
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	r.mu.Lock()
	prev := r.syncErr
	r.syncErr = msg
	r.mu.Unlock()
	switch {
	case msg != "" && msg != prev:
		log.Printf("sync: %s", msg)
	case msg == "" && prev != "":
		log.Printf("sync: ok again")
	}
}

func (r *Relay) refresh() {
	set, err := r.store.Settings()
	if err != nil {
		return
	}
	m := admin.Manager(set, r.cfg.Runner, r.ipv6)
	peers := map[string]wg.Peer{}
	for _, c := range r.store.All() {
		name := set.Iface(c.Slot)
		ps, err := m.Peers(name)
		if err != nil {
			continue
		}
		for _, p := range ps {
			if p.Key == c.PublicKey {
				peers[name] = p
			}
		}
	}
	r.mu.Lock()
	r.peers = peers
	r.mu.Unlock()
}

// Snapshot is the live view the TUI reads.
func (r *Relay) Snapshot() api.Snapshot {
	set, _ := r.store.Settings()
	pub, _ := wg.PublicKey(set.PrivateKey)
	r.mu.Lock()
	defer r.mu.Unlock()

	snap := api.Snapshot{
		Endpoint:  set.Endpoint,
		Kind:      set.Kind,
		Version:   Version,
		PublicKey: pub,
		StartedAt: r.started,
		SyncError: r.syncErr,
	}
	for _, c := range r.store.All() {
		info := api.Client{
			Name:    c.Name,
			Status:  c.Status,
			Iface:   set.Iface(c.Slot),
			Port:    set.Port(c.Slot),
			Mark:    set.Mark(c.Slot),
			Address: set.NodeAddr(c.Slot).Addr().String(),
		}
		if p, ok := r.peers[info.Iface]; ok {
			info.Endpoint = p.Endpoint
			info.Handshake = p.Handshake
			info.Rx, info.Tx = p.Rx, p.Tx
			info.Online = !p.Handshake.IsZero() && time.Since(p.Handshake) < onlineWindow
		}
		snap.Clients = append(snap.Clients, info)
	}
	return snap
}

// serveSocket publishes snapshots on a unix socket. One request, one
// document, then close, so there is no protocol to get wrong.
func (r *Relay) serveSocket(path string) {
	os.Remove(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
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

			// Three clients share this socket, a bare snapshot reader, a
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
			json.NewEncoder(c).Encode(r.Snapshot())
		}(c)
	}
}

// runCommand is the write half of the socket. The file watch cannot see this
// process's own writes, so it syncs the kernel explicitly.
func (r *Relay) runCommand(cmd api.Command) api.Response {
	var (
		cl  store.Client
		err error
		msg string
	)
	switch cmd.Cmd {
	case "approve":
		if cl, err = r.admin.Approve(cmd.Name); err == nil {
			msg = cl.Name + " active"
		}
	case "revoke":
		if cl, err = r.admin.Revoke(cmd.Name); err == nil {
			msg = cl.Name + " revoked"
		}
	default:
		err = fmt.Errorf("unknown command %q", cmd.Cmd)
	}

	if err != nil {
		log.Printf("command %s %s: %v", cmd.Cmd, cmd.Name, err)
		return api.Response{OK: false, Message: err.Error(), Snapshot: ptr(r.Snapshot())}
	}
	log.Printf("%s", msg)
	r.sync()
	r.refresh()
	return api.Response{OK: true, Message: msg, Snapshot: ptr(r.Snapshot())}
}

func ptr(s api.Snapshot) *api.Snapshot { return &s }
