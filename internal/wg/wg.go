// Package wg drives the kernel side of the relay through `ip` and `awg`/`wg`.
// Every operation is idempotent, so Sync can run on a timer and heal drift.
package wg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	KindAWG = "awg"
	KindWG  = "wg"

	// RulePref is the priority of every ip rule the relay owns. Rules at this
	// priority whose table is not wanted are deleted, others are never touched.
	RulePref = 7700
)

type Runner interface {
	Run(stdin, name string, args ...string) (string, error)
}

type Exec struct{}

func (Exec) Run(stdin, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %v: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Link is one node's interface on the relay. Traffic carrying Mark is routed
// into it, and its only peer gets 0.0.0.0/0.
type Link struct {
	Name    string
	Port    int
	Mark    int
	Address netip.Prefix // relay end, e.g. 10.77.0.5/30
	Peer    string       // node public key, empty while pending or revoked
}

type Manager struct {
	Run        Runner
	Kind       string
	Prefix     string // only links named Prefix+digits are ours
	PrivateKey string
	Params     *Params // nil for plain WireGuard
	IPv6       bool    // false when the kernel has no IPv6, so there is nothing to leak
}

func (m *Manager) tool() string {
	if m.Kind == KindWG {
		return "wg"
	}
	return "awg"
}

func (m *Manager) linkType() string {
	if m.Kind == KindWG {
		return "wireguard"
	}
	return "amneziawg"
}

func (m *Manager) ours(name string) bool {
	rest, ok := strings.CutPrefix(name, m.Prefix)
	if !ok || rest == "" {
		return false
	}
	_, err := strconv.Atoi(rest)
	return err == nil
}

// Sync makes the kernel match want and removes links and rules nobody wants.
func (m *Manager) Sync(want []Link) error {
	have, err := m.links()
	if err != nil {
		return err
	}

	var errs []error
	wanted := map[string]bool{}
	marks := map[int]bool{}
	for _, l := range want {
		wanted[l.Name] = true
		marks[l.Mark] = true
		if err := m.apply(l, have[l.Name]); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", l.Name, err))
		}
	}
	for name := range have {
		if !wanted[name] {
			if _, err := m.Run.Run("", "ip", "link", "del", name); err != nil {
				errs = append(errs, err)
			}
		}
	}

	families := []string{"-4"}
	if m.IPv6 {
		families = append(families, "-6")
	}
	for _, fam := range families {
		if err := m.syncRules(fam, marks); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) apply(l Link, exists bool) error {
	r := m.Run
	if !exists {
		if _, err := r.Run("", "ip", "link", "add", l.Name, "type", m.linkType()); err != nil {
			return err
		}
	}

	args := []string{"set", l.Name, "private-key", "/dev/stdin", "listen-port", strconv.Itoa(l.Port)}
	if m.Params != nil {
		args = append(args, m.Params.Args()...)
	}
	if _, err := r.Run(m.PrivateKey, m.tool(), args...); err != nil {
		return err
	}

	out, err := r.Run("", m.tool(), "show", l.Name, "peers")
	if err != nil {
		return err
	}
	for _, p := range strings.Fields(out) {
		if p != l.Peer {
			if _, err := r.Run("", m.tool(), "set", l.Name, "peer", p, "remove"); err != nil {
				return err
			}
		}
	}
	if l.Peer != "" {
		if _, err := r.Run("", m.tool(), "set", l.Name, "peer", l.Peer, "allowed-ips", "0.0.0.0/0"); err != nil {
			return err
		}
	}

	table := strconv.Itoa(l.Mark)
	// Replies come in from sources the main table routes via eth0, so strict
	// rp_filter drops them. The kernel uses max(all, iface), so 2 means loose.
	cmds := [][]string{
		{"ip", "addr", "replace", l.Address.String(), "dev", l.Name},
		{"sysctl", "-qw", "net.ipv4.conf." + l.Name + ".rp_filter=2"},
		{"ip", "link", "set", l.Name, "up"},
		{"ip", "route", "replace", "default", "dev", l.Name, "src", l.Address.Addr().String(), "table", table},
	}
	// The node is IPv4 only. Without this, marked IPv6 traffic would fall
	// through to the main table and leave from the relay's own address.
	if m.IPv6 {
		cmds = append(cmds, []string{"ip", "-6", "route", "replace", "unreachable", "default", "table", table})
	}
	for _, c := range cmds {
		if _, err := r.Run("", c[0], c[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) links() (map[string]bool, error) {
	out, err := m.Run.Run("", "ip", "-j", "link", "show", "type", m.linkType())
	if err != nil {
		return nil, err
	}
	var ls []struct {
		Name string `json:"ifname"`
	}
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &ls); err != nil {
			return nil, fmt.Errorf("ip link: %w", err)
		}
	}
	have := map[string]bool{}
	for _, l := range ls {
		if m.ours(l.Name) {
			have[l.Name] = true
		}
	}
	return have, nil
}

type rule struct {
	Priority int    `json:"priority"`
	FwMark   string `json:"fwmark"`
	Table    string `json:"table"`
}

func (m *Manager) syncRules(fam string, marks map[int]bool) error {
	out, err := m.Run.Run("", "ip", "-j", fam, "rule", "show")
	if err != nil {
		return err
	}
	var rules []rule
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &rules); err != nil {
			return fmt.Errorf("ip rule: %w", err)
		}
	}

	var errs []error
	present := map[int]bool{}
	for _, ru := range rules {
		if ru.Priority != RulePref {
			continue
		}
		table, err := strconv.Atoi(ru.Table)
		mark, merr := strconv.ParseInt(ru.FwMark, 0, 64)
		if err == nil && merr == nil && int(mark) == table && marks[table] {
			present[table] = true
			continue
		}
		args := []string{fam, "rule", "del", "pref", strconv.Itoa(RulePref)}
		if ru.FwMark != "" {
			args = append(args, "fwmark", ru.FwMark)
		}
		if ru.Table != "" {
			args = append(args, "table", ru.Table)
		}
		if _, err := m.Run.Run("", "ip", args...); err != nil {
			errs = append(errs, err)
			continue
		}
		m.Run.Run("", "ip", fam, "route", "flush", "table", ru.Table)
	}
	for mark := range marks {
		if present[mark] {
			continue
		}
		s := strconv.Itoa(mark)
		if _, err := m.Run.Run("", "ip", fam, "rule", "add", "fwmark", s, "table", s, "pref", strconv.Itoa(RulePref)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type Peer struct {
	Key       string
	Endpoint  string
	Handshake time.Time
	Rx, Tx    int64
}

// Peers reads `show <link> dump`. The first line is the interface, whose
// column count differs between wg and awg, so it is skipped rather than parsed.
func (m *Manager) Peers(link string) ([]Peer, error) {
	out, err := m.Run.Run("", m.tool(), "show", link, "dump")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var ps []Peer
	for _, ln := range lines[1:] {
		f := strings.Split(ln, "\t")
		if len(f) < 8 {
			continue
		}
		p := Peer{Key: f[0], Endpoint: f[2]}
		if p.Endpoint == "(none)" {
			p.Endpoint = ""
		}
		if ts, _ := strconv.ParseInt(f[4], 10, 64); ts > 0 {
			p.Handshake = time.Unix(ts, 0)
		}
		p.Rx, _ = strconv.ParseInt(f[5], 10, 64)
		p.Tx, _ = strconv.ParseInt(f[6], 10, 64)
		ps = append(ps, p)
	}
	return ps, nil
}
