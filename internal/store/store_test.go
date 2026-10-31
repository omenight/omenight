package store

import (
	"path/filepath"
	"testing"

	"omenight/internal/wg"
)

func settings(t *testing.T) Settings {
	t.Helper()
	key, err := wg.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return Settings{
		Kind: wg.KindAWG, Endpoint: "home.omenight.com", Net: "10.77.0.0/16",
		PortBase: 51820, MarkBase: 7700, Prefix: "om", PrivateKey: key,
	}
}

func TestSlotDerivation(t *testing.T) {
	s := settings(t)
	if got := s.RelayAddr(1).String(); got != "10.77.0.5/30" {
		t.Fatalf("relay addr %s", got)
	}
	if got := s.NodeAddr(1).String(); got != "10.77.0.6/30" {
		t.Fatalf("node addr %s", got)
	}
	if got := s.NodeAddr(64).String(); got != "10.77.1.2/30" {
		t.Fatalf("slot 64 crosses into the next /24, got %s", got)
	}
	if s.Iface(3) != "om3" || s.Port(3) != 51823 || s.Mark(3) != 7703 {
		t.Fatalf("slot 3: %s %d %d", s.Iface(3), s.Port(3), s.Mark(3))
	}
	if got := s.EndpointFor(1); got != "home.omenight.com:51821" {
		t.Fatalf("endpoint %s", got)
	}
	s.Net = "10.77.0.0/28"
	if got := s.Slots(); got != 3 {
		t.Fatalf("a /28 holds slots 1-3, got %d", got)
	}
}

func TestValidate(t *testing.T) {
	for name, mut := range map[string]func(*Settings){
		"kind":     func(s *Settings) { s.Kind = "ipsec" },
		"port":     func(s *Settings) { s.Endpoint = "home.omenight.com:443" },
		"quote":    func(s *Settings) { s.Endpoint = "a'b" },
		"v6 net":   func(s *Settings) { s.Net = "fd00::/64" },
		"tiny net": func(s *Settings) { s.Net = "10.0.0.0/30" },
		"key":      func(s *Settings) { s.PrivateKey = "nope" },
	} {
		s := settings(t)
		mut(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
	s := settings(t)
	s.Endpoint = "130.17.11.31"
	if err := s.Validate(); err != nil {
		t.Fatalf("IP endpoint rejected: %v", err)
	}
}

func TestClientFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	daemon, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.Add("a", "k", "p"); err != ErrNoSettings {
		t.Fatalf("add before init: %v", err)
	}
	if err := daemon.Init(settings(t)); err != nil {
		t.Fatal(err)
	}
	if err := daemon.Init(settings(t)); err == nil {
		t.Fatal("second init must fail, it would move every node")
	}

	// The CLI is a separate process that opens the file, mutates and saves.
	cli, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b", "c"} {
		if _, err := cli.Add(n, "priv-"+n, "pub-"+n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cli.Add("b", "x", "y"); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := cli.Add("bad name", "x", "y"); err == nil {
		t.Fatal("name with a space accepted")
	}
	if _, err := cli.Remove("b"); err != nil {
		t.Fatal(err)
	}
	d, err := cli.Add("d", "priv-d", "pub-d")
	if err != nil {
		t.Fatal(err)
	}
	if d.Slot != 2 {
		t.Fatalf("freed slot 2 should be reused, got %d", d.Slot)
	}
	if _, err := cli.SetStatus("c", StatusRevoked); err != nil {
		t.Fatal(err)
	}

	changed, err := daemon.Reload()
	if err != nil || !changed {
		t.Fatalf("daemon should notice the CLI's write: %v %v", changed, err)
	}
	c, ok := daemon.ByName("c")
	if !ok || c.Status != StatusRevoked || c.Slot != 3 {
		t.Fatalf("reload lost state: %+v", c)
	}
	if n := len(daemon.All()); n != 3 {
		t.Fatalf("want 3 clients, got %d", n)
	}
}
