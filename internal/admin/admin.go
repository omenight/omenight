package admin

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"omenight/internal/store"
)

// DefaultPortBase is where listen-port allocation starts. Ports are handed
// out sequentially and never reused while the client exists.
const DefaultPortBase = 20000

const portSpan = 1000

// Admin is the control plane: the operations behind the CLI and the TUI.
// Both go through it so a port is never allocated twice.
type Admin struct {
	st   *store.Store
	base int
}

func New(st *store.Store, base int) *Admin {
	if base == 0 {
		base = DefaultPortBase
	}
	return &Admin{st: st, base: base}
}

// Approve activates a client and, if it has no listen address yet, gives it
// the lowest free port from the allocation range. The port is persisted, so
// it survives reconnects and restarts.
func (a *Admin) Approve(name string) (store.Client, error) {
	c, ok := a.st.ByName(name)
	if !ok {
		return store.Client{}, fmt.Errorf("%s: no such client (has it connected yet?)", name)
	}
	if len(c.Allow) == 0 {
		addr, err := a.allocate()
		if err != nil {
			return store.Client{}, err
		}
		if c, err = a.st.Allow(name, addr); err != nil {
			return store.Client{}, err
		}
	}
	return a.st.SetStatus(name, store.StatusActive)
}

func (a *Admin) Revoke(name string) (store.Client, error) {
	if _, ok := a.st.ByName(name); !ok {
		return store.Client{}, fmt.Errorf("%s: no such client", name)
	}
	return a.st.SetStatus(name, store.StatusRevoked)
}

// Allow grants an extra listen address on top of the assigned one.
func (a *Admin) Allow(name, addr string) (store.Client, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return store.Client{}, fmt.Errorf("bad address %q", addr)
	}
	return a.st.Allow(name, addr)
}

// allocate finds a port nobody else is using and that can actually be bound.
func (a *Admin) allocate() (string, error) {
	used := map[int]bool{}
	for _, c := range a.st.All() {
		for _, addr := range c.Allow {
			if host, port, err := net.SplitHostPort(addr); err == nil && host == "127.0.0.1" {
				if n, err := strconv.Atoi(port); err == nil {
					used[n] = true
				}
			}
		}
	}
	for i := 0; i < 64; i++ {
		p := store.NextFreePort(a.base, portSpan, used)
		if p == 0 {
			return "", errors.New("no free port left in the allocation range")
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			ln.Close()
			return fmt.Sprintf("127.0.0.1:%d", p), nil
		}
		used[p] = true
	}
	return "", errors.New("no free port left in the allocation range")
}
