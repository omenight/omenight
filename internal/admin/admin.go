package admin

import (
	"fmt"
	"strings"

	"omenight"
	"omenight/internal/store"
	"omenight/internal/wg"
)

const (
	DefaultNet      = "10.77.0.0/16"
	DefaultPortBase = 51820
	DefaultMarkBase = 7700
	DefaultPrefix   = "om"
)

// Admin is the control plane, the operations behind the CLI and the TUI.
// Both go through it, so keys and slots are made in one place.
type Admin struct {
	st *store.Store
}

func New(st *store.Store) *Admin { return &Admin{st: st} }

// Init creates the relay key and, for AWG, the obfuscation params every node
// will share.
func (a *Admin) Init(set store.Settings) (store.Settings, error) {
	key, err := wg.GenerateKey()
	if err != nil {
		return set, err
	}
	set.PrivateKey = key
	if set.Kind == wg.KindAWG {
		p, err := wg.NewParams()
		if err != nil {
			return set, err
		}
		set.AWG = &p
	}
	return set, a.st.Init(set)
}

// Add makes the node's key pair here, so the paste block is all the node
// needs and nothing travels back.
func (a *Admin) Add(name string) (store.Client, error) {
	priv, err := wg.GenerateKey()
	if err != nil {
		return store.Client{}, err
	}
	pub, err := wg.PublicKey(priv)
	if err != nil {
		return store.Client{}, err
	}
	return a.st.Add(name, priv, pub)
}

func (a *Admin) Approve(name string) (store.Client, error) {
	return a.st.SetStatus(name, store.StatusActive)
}

func (a *Admin) Revoke(name string) (store.Client, error) {
	return a.st.SetStatus(name, store.StatusRevoked)
}

func (a *Admin) Remove(name string) (store.Client, error) {
	return a.st.Remove(name)
}

// Links is what the kernel should look like. A revoked node keeps its
// interface without a peer, so traffic marked for it is dropped, not leaked.
func Links(set store.Settings, clients []store.Client) []wg.Link {
	out := make([]wg.Link, 0, len(clients))
	for _, c := range clients {
		l := wg.Link{
			Name:    set.Iface(c.Slot),
			Port:    set.Port(c.Slot),
			Mark:    set.Mark(c.Slot),
			Address: set.RelayAddr(c.Slot),
		}
		if c.Status == store.StatusActive {
			l.Peer = c.PublicKey
		}
		out = append(out, l)
	}
	return out
}

func Manager(set store.Settings, run wg.Runner, ipv6 bool) *wg.Manager {
	return &wg.Manager{
		Run:        run,
		Kind:       set.Kind,
		Prefix:     set.Prefix,
		PrivateKey: set.PrivateKey,
		Params:     set.AWG,
		IPv6:       ipv6,
	}
}

// Conf is the node's wg-quick file. Table = off and a /32 AllowedIPs keep
// the node's own traffic out of the tunnel, it only ever answers the relay.
func Conf(set store.Settings, c store.Client) (string, error) {
	relayPub, err := wg.PublicKey(set.PrivateKey)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# omenight node %s\n", c.Name)
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s\nTable = off\n", c.PrivateKey, set.NodeAddr(c.Slot))
	if p := set.AWG; p != nil {
		fmt.Fprintf(&b, "Jc = %d\nJmin = %d\nJmax = %d\nS1 = %d\nS2 = %d\nH1 = %d\nH2 = %d\nH3 = %d\nH4 = %d\n",
			p.Jc, p.Jmin, p.Jmax, p.S1, p.S2, p.H1, p.H2, p.H3, p.H4)
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s/32\nPersistentKeepalive = 25\n",
		relayPub, set.EndpointFor(c.Slot), set.RelayAddr(c.Slot).Addr())
	return b.String(), nil
}

// Delim closes the paste heredoc, so it must never be a line of the installer.
const Delim = "OMENIGHT"

// Paste is the block to paste into a root shell on the node. It is plain text
// because OpenWrt's busybox has no base64.
func Paste(set store.Settings, c store.Client) (string, error) {
	conf, err := Conf(set, c)
	if err != nil {
		return "", err
	}
	return wrap("OMENIGHT_CONF='" + conf + "'"), nil
}

// UninstallPaste undoes Paste on the node.
func UninstallPaste() string { return wrap("OMENIGHT_ACTION=uninstall") }

func wrap(head string) string {
	var b strings.Builder
	b.WriteString("sh <<'" + Delim + "'\n" + head + "\n")
	for _, ln := range strings.Split(omenight.InstallScript, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		b.WriteString(ln + "\n")
	}
	b.WriteString(Delim + "\n")
	return b.String()
}

// Xray is the outbound that sends a client's traffic out through this node.
func Xray(set store.Settings, c store.Client) string {
	return fmt.Sprintf(`{
  "tag": %q,
  "protocol": "freedom",
  "settings": {"domainStrategy": "UseIPv4"},
  "streamSettings": {"sockopt": {"mark": %d}}
}`, c.Name, set.Mark(c.Slot))
}
