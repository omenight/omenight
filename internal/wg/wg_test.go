package wg

import (
	"net/netip"
	"strings"
	"testing"
)

func TestPublicKeyVector(t *testing.T) {
	// RFC 7748 section 6.1, Alice, in base64.
	pub, err := PublicKey("dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo=")
	if err != nil {
		t.Fatal(err)
	}
	if pub != "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo=" {
		t.Fatalf("got %s", pub)
	}
}

func TestGenerateKeyIsClamped(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := decodeKey(k)
	if b[0]&7 != 0 || b[31]&128 != 0 || b[31]&64 == 0 {
		t.Fatalf("not clamped: %x", b)
	}
}

func TestParams(t *testing.T) {
	for i := 0; i < 200; i++ {
		p, err := NewParams()
		if err != nil {
			t.Fatal(err)
		}
		if p.S1+56 == p.S2 || p.Jmin >= p.Jmax || p.H1 < 5 {
			t.Fatalf("bad params %+v", p)
		}
		hs := map[uint32]bool{p.H1: true, p.H2: true, p.H3: true, p.H4: true}
		if len(hs) != 4 {
			t.Fatalf("headers collide %+v", p)
		}
		if n := len(strings.Split(p.String(), ",")); n != 9 {
			t.Fatalf("String has %d fields", n)
		}
	}
}

// fake answers the read commands from canned output and records the rest.
type fake struct {
	out  map[string]string
	ran  []string
	keys []string
}

func (f *fake) Run(stdin, name string, args ...string) (string, error) {
	cmd := name + " " + strings.Join(args, " ")
	if stdin != "" {
		f.keys = append(f.keys, stdin)
	}
	if out, ok := f.out[cmd]; ok {
		return out, nil
	}
	f.ran = append(f.ran, cmd)
	return "", nil
}

func (f *fake) did(cmd string) bool {
	for _, c := range f.ran {
		if c == cmd {
			return true
		}
	}
	return false
}

func TestSync(t *testing.T) {
	f := &fake{out: map[string]string{
		"ip -j link show type amneziawg": `[{"ifname":"om1"},{"ifname":"om9"},{"ifname":"awg0"},{"ifname":"omx"}]`,
		"awg show om1 peers":             "STALEKEY\nNODEKEY\n",
		"awg show om2 peers":             "",
		"ip -j -4 rule show": `[{"priority":0,"table":"local"},
			{"priority":7700,"fwmark":"0x1e15","table":"7701"},
			{"priority":7700,"fwmark":"0x1e1d","table":"7709"},
			{"priority":32766,"table":"main"}]`,
		"ip -j -6 rule show": `[]`,
	}}
	m := &Manager{Run: f, Kind: KindAWG, Prefix: "om", PrivateKey: "RELAYKEY", Params: &Params{Jc: 4}, IPv6: true}
	err := m.Sync([]Link{
		{Name: "om1", Port: 51821, Mark: 7701, Address: netip.MustParsePrefix("10.77.0.5/30"), Peer: "NODEKEY"},
		{Name: "om2", Port: 51822, Mark: 7702, Address: netip.MustParsePrefix("10.77.0.9/30")},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"ip link add om2 type amneziawg",
		"awg set om1 peer STALEKEY remove",
		"awg set om1 peer NODEKEY allowed-ips 0.0.0.0/0",
		"ip addr replace 10.77.0.5/30 dev om1",
		"sysctl -qw net.ipv4.conf.om1.rp_filter=2",
		"ip route replace default dev om1 src 10.77.0.5 table 7701",
		"ip -6 route replace unreachable default table 7702",
		"ip link del om9",
		"ip -4 rule del pref 7700 fwmark 0x1e1d table 7709",
		"ip -4 route flush table 7709",
		"ip -4 rule add fwmark 7702 table 7702 pref 7700",
		"ip -6 rule add fwmark 7701 table 7701 pref 7700",
	} {
		if !f.did(want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, never := range []string{
		"ip link add om1 type amneziawg",
		"ip link del awg0",
		"ip link del omx",
		"ip -4 rule add fwmark 7701 table 7701 pref 7700",
		"awg set om2 peer  allowed-ips 0.0.0.0/0",
	} {
		if f.did(never) {
			t.Errorf("should not run %q", never)
		}
	}
	for _, c := range f.ran {
		if strings.Contains(c, "RELAYKEY") {
			t.Fatalf("private key on a command line: %s", c)
		}
	}
	if len(f.keys) != 2 || f.keys[0] != "RELAYKEY" {
		t.Fatalf("private key must go through stdin, got %v", f.keys)
	}
}

func TestPeers(t *testing.T) {
	f := &fake{out: map[string]string{
		"awg show om1 dump": "PRIV\tPUB\t51821\toff\t4\t40\t70\t20\t90\t1\t2\t3\t4\n" +
			"NODEKEY\t(none)\t46.138.79.146:41820\t0.0.0.0/0\t1793450000\t1200\t3400\toff\n" +
			"IDLE\t(none)\t(none)\t0.0.0.0/0\t0\t0\t0\toff\n",
	}}
	ps, err := (&Manager{Run: f, Kind: KindAWG}).Peers("om1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("want 2 peers, got %+v", ps)
	}
	if p := ps[0]; p.Key != "NODEKEY" || p.Endpoint != "46.138.79.146:41820" ||
		p.Handshake.Unix() != 1793450000 || p.Rx != 1200 || p.Tx != 3400 {
		t.Fatalf("bad peer %+v", p)
	}
	if p := ps[1]; p.Endpoint != "" || !p.Handshake.IsZero() {
		t.Fatalf("idle peer %+v", p)
	}
}
