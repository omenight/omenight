package tui

import (
	"strings"
	"testing"
	"time"

	"omenight/internal/api"
)

func TestView(t *testing.T) {
	m := Model{}
	m.snap = &api.Snapshot{
		Endpoint:  "home.omenight.com",
		Kind:      "awg",
		PublicKey: "relaypubkey=",
		StartedAt: time.Now().Add(-90 * time.Second),
		SyncError: "om3: ip link add failed",
		Clients: []api.Client{
			{
				Name: "node-a", Status: "active", Online: true,
				Iface: "om1", Address: "10.77.0.6", Endpoint: "46.138.79.146:41820",
				Rx: 6750, Tx: 2 << 20, Handshake: time.Now().Add(-40 * time.Second),
			},
			{
				Name: "node-b", Status: "active", Online: false,
				Iface: "om2", Address: "10.77.0.10",
			},
			{
				Name: "node-c", Status: "revoked", Iface: "om3",
			},
		},
	}

	out := m.View()

	for _, want := range []string{
		"omenight relay", "home.omenight.com (awg)", "relaypubkey=",
		"sync: om3: ip link add failed",
		"node-a", "active", "yes", "om1", "10.77.0.6", "46.138.79.146:41820", "40s ago",
		"node-b", "no", "om2",
		"node-c", "revoked",
		"6.6 KB", "2.0 MB",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q\n%s", want, out)
		}
	}
}

func TestHuman(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		999:     "999 B",
		2048:    "2.0 KB",
		2 << 20: "2.0 MB",
		5 << 30: "5.0 GB",
	}
	for in, want := range cases {
		if got := human(in); got != want {
			t.Errorf("human(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSelection(t *testing.T) {
	m := Model{}
	m.snap = &api.Snapshot{Clients: []api.Client{{Name: "a"}, {Name: "b"}}}

	if got := m.selected(); got != "a" {
		t.Fatalf("want a, got %q", got)
	}
	m.cursor = 1
	if got := m.selected(); got != "b" {
		t.Fatalf("want b, got %q", got)
	}
	m.cursor = 9
	m.clamp()
	if m.cursor != 1 {
		t.Fatalf("cursor not clamped: %d", m.cursor)
	}
	m.snap.Clients = nil
	m.clamp()
	if m.cursor != 0 || m.selected() != "" {
		t.Fatalf("empty list should reset the cursor")
	}
}

func TestViewMarksCursor(t *testing.T) {
	m := Model{}
	m.snap = &api.Snapshot{
		Endpoint: "home.omenight.com",
		Clients:  []api.Client{{Name: "node-a", Status: "active"}, {Name: "node-b", Status: "revoked"}},
	}
	out := m.View()

	if !strings.Contains(out, "› node-a") {
		t.Fatalf("cursor marker missing from first row:\n%s", out)
	}
	if strings.Contains(out, "› node-b") {
		t.Fatalf("cursor marker on the wrong row:\n%s", out)
	}
	for _, want := range []string{"a approve", "x revoke", "j/k move"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q", want)
		}
	}
}
