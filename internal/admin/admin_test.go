package admin

import (
	"path/filepath"
	"strings"
	"testing"

	"omenight/internal/store"
	"omenight/internal/wg"
)

func setup(t *testing.T, kind string) (*Admin, store.Settings) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := New(st)
	set, err := a.Init(store.Settings{
		Kind: kind, Endpoint: "home.omenight.com", Net: DefaultNet,
		PortBase: DefaultPortBase, MarkBase: DefaultMarkBase, Prefix: DefaultPrefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, set
}

func TestConf(t *testing.T) {
	a, set := setup(t, wg.KindAWG)
	c, err := a.Add("archer-ax23")
	if err != nil {
		t.Fatal(err)
	}
	conf, err := Conf(set, c)
	if err != nil {
		t.Fatal(err)
	}
	relayPub, _ := wg.PublicKey(set.PrivateKey)
	for _, want := range []string{
		"# omenight node archer-ax23\n",
		"PrivateKey = " + c.PrivateKey + "\n",
		"Address = 10.77.0.6/30\n",
		"Table = off\n",
		"H4 = ",
		"PublicKey = " + relayPub + "\n",
		"Endpoint = home.omenight.com:51821\n",
		"AllowedIPs = 10.77.0.5/32\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf missing %q\n%s", want, conf)
		}
	}

	_, plain := setup(t, wg.KindWG)
	conf, _ = Conf(plain, c)
	if strings.Contains(conf, "Jc =") {
		t.Fatalf("plain WireGuard conf has AWG params\n%s", conf)
	}
}

func TestPaste(t *testing.T) {
	a, set := setup(t, wg.KindAWG)
	c, _ := a.Add("archer-ax23")
	p, err := Paste(set, c)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(p, "\n"), "\n")
	if lines[0] != "sh <<'OMENIGHT'" || lines[len(lines)-1] != Delim {
		t.Fatalf("bad framing: %q ... %q", lines[0], lines[len(lines)-1])
	}
	for _, ln := range lines[1 : len(lines)-1] {
		if ln == Delim {
			t.Fatal("the installer contains the heredoc delimiter")
		}
	}
	if !strings.Contains(p, "OMENIGHT_CONF='# omenight node archer-ax23\n") {
		t.Fatal("conf not embedded")
	}
	if !strings.Contains(p, `main "$@" </dev/null`) {
		t.Fatal("installer body missing")
	}
	if strings.Contains(p, "#!/bin/sh") {
		t.Fatal("comments should be stripped")
	}
}

func TestLinksRevoked(t *testing.T) {
	a, set := setup(t, wg.KindAWG)
	a.Add("n1")
	a.Add("n2")
	a.Revoke("n2")
	ls := Links(set, a.st.All())
	if len(ls) != 2 || ls[0].Peer == "" || ls[1].Peer != "" {
		t.Fatalf("revoked node must keep its link without a peer: %+v", ls)
	}
	if ls[1].Name != "om2" || ls[1].Mark != 7702 {
		t.Fatalf("bad link %+v", ls[1])
	}
}
