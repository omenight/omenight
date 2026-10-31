package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"omenight/internal/identity"
	"omenight/internal/proto"
	"omenight/internal/xio"
)

type Tunnel struct {
	Listen string `json:"listen"` // address to bind on the relay
	Target string `json:"target"` // address to dial from this node
}

type Config struct {
	Name       string   `json:"name"`
	Relay      string   `json:"relay"`
	KeyPath    string   `json:"key,omitempty"`
	KnownHosts string   `json:"known_hosts,omitempty"`
	Socks      string   `json:"socks,omitempty"`
	Tunnels    []Tunnel `json:"forwards,omitempty"`
	Verbose    bool     `json:"verbose,omitempty"`
}

// LoadConfig reads the file the installer writes.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

type Agent struct {
	cfg    Config
	signer ssh.Signer
}

func New(cfg Config) (*Agent, error) {
	signer, err := identity.LoadOrCreate(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	return &Agent{cfg: cfg, signer: signer}, nil
}

func (a *Agent) PublicKey() string { return identity.AuthorizedKey(a.signer) }

// Run reconnects forever with capped exponential backoff and jitter.
func (a *Agent) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		started := time.Now()
		err := a.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		jitter := time.Duration(rand.Int63n(int64(backoff)/4 + 1))
		log.Printf("session: %v (retry in %s)", err, (backoff + jitter).Round(time.Second))

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff + jitter):
		}
		backoff *= 2
		if backoff > time.Minute {
			backoff = time.Minute
		}
	}
}

type session struct {
	conn ssh.Conn
	cfg  Config

	mu      sync.Mutex
	targets map[string]string // relay listen addr -> local target
	conns   int64
}

func (a *Agent) runOnce(ctx context.Context) error {
	nc, err := net.DialTimeout("tcp", a.cfg.Relay, 15*time.Second)
	if err != nil {
		return err
	}
	defer nc.Close()

	cc, chans, reqs, err := ssh.NewClientConn(nc, a.cfg.Relay, &ssh.ClientConfig{
		User:            a.cfg.Name,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(a.signer)},
		HostKeyCallback: identity.HostKeyChecker(a.cfg.KnownHosts),
		ClientVersion:   "SSH-2.0-omenight_" + proto.Version,
		Timeout:         15 * time.Second,
	})
	if err != nil {
		return err
	}

	s := &session{conn: cc, cfg: a.cfg, targets: map[string]string{}}
	for _, t := range a.cfg.Tunnels {
		s.targets[t.Listen] = t.Target
	}

	go s.handleChannels(chans)
	go s.handleRequests(reqs)
	go s.keepalive(ctx)

	ctrl, ctrlReqs, err := cc.OpenChannel(proto.ControlChannel, nil)
	if err != nil {
		return err
	}
	defer ctrl.Close()
	go ssh.DiscardRequests(ctrlReqs)

	host, _ := os.Hostname()
	hello := proto.Hello{
		Version:  proto.Version,
		Hostname: host,
		OS:       runtime.GOOS + "/" + runtime.GOARCH,
	}
	for _, t := range a.cfg.Tunnels {
		hello.Tunnels = append(hello.Tunnels, t.Listen)
	}
	if err := proto.WriteMsg(ctrl, proto.Msg{Type: proto.TypeHello, Hello: &hello}); err != nil {
		return err
	}

	rd := proto.NewMsgReader(ctrl)
	msg, err := rd.Next()
	if err != nil {
		return err
	}
	if msg.Config == nil {
		return fmt.Errorf("expected config, got %q", msg.Type)
	}

	if len(msg.Config.Granted) == 0 {
		log.Printf("connected but nothing granted (%s)", msg.Config.Reason)
	} else {
		log.Printf("connected, granted %v", msg.Config.Granted)
	}

	for _, addr := range msg.Config.Granted {
		if err := s.forward(addr); err != nil {
			log.Printf("forward %s: %v", addr, err)
		}
	}

	hb := time.Duration(msg.Config.HeartbeatSec) * time.Second
	if hb <= 0 {
		hb = 15 * time.Second
	}
	go s.heartbeat(ctx, ctrl, hb)

	cc.Wait()
	return fmt.Errorf("connection closed")
}

func (s *session) forward(addr string) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}
	ok, _, err := s.conn.SendRequest("tcpip-forward", true, ssh.Marshal(proto.TCPIPForwardRequest{
		BindAddr: host,
		BindPort: uint32(port),
	}))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("relay refused %s", addr)
	}
	log.Printf("relay now listening on %s", addr)
	return nil
}

// handleChannels turns each forwarded connection into a dial to the local
// target. The relay never learns the target.
func (s *session) handleChannels(chans <-chan ssh.NewChannel) {
	for nc := range chans {
		if nc.ChannelType() != "forwarded-tcpip" {
			nc.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		var p proto.ForwardedTCPIPPayload
		if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
			nc.Reject(ssh.ConnectionFailed, "bad payload")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(chReqs)

		addr := net.JoinHostPort(p.Addr, strconv.Itoa(int(p.Port)))
		target, ok := s.targets[addr]
		if !ok {
			ch.Close()
			continue
		}
		go s.dial(ch, target, p.OriginAddr)
	}
}

func (s *session) dial(ch ssh.Channel, target, origin string) {
	defer ch.Close()

	up, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		if s.cfg.Verbose {
			log.Printf("%s -> %s: %v", origin, target, err)
		}
		return
	}
	defer up.Close()

	s.mu.Lock()
	s.conns++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.conns--
		s.mu.Unlock()
	}()

	if s.cfg.Verbose {
		log.Printf("%s -> %s", origin, target)
	}
	xio.Splice(ch, up)
}

func (s *session) handleRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		req.Reply(true, nil)
	}
}

// keepalive catches a dead relay: SSH requests have no timeout of their own.
func (s *session) keepalive(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			done := make(chan error, 1)
			go func() {
				_, _, err := s.conn.SendRequest(proto.Keepalive, true, nil)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					s.conn.Close()
					return
				}
			case <-time.After(10 * time.Second):
				log.Printf("keepalive timeout, dropping")
				s.conn.Close()
				return
			}
		}
	}
}

func (s *session) heartbeat(ctx context.Context, ch ssh.Channel, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			n := s.conns
			s.mu.Unlock()
			err := proto.WriteMsg(ch, proto.Msg{Type: proto.TypePing, Ping: &proto.Ping{
				TS:    time.Now().Unix(),
				Conns: n,
			}})
			if err != nil {
				return
			}
		}
	}
}
