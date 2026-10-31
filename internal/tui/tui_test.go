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
		Listen:    "0.0.0.0:2223",
		HostKey:   "SHA256:abcdef",
		StartedAt: time.Now().Add(-90 * time.Second),
		Clients: []api.Client{
			{
				Name: "node-a", Status: "active", Online: true,
				Conns: 2, Up: 6750, Down: 2 << 20,
				Allow: []string{"127.0.0.1:1085"},
			},
			{
				Name: "node-b", Status: "pending", Online: false,
				LastSeen: time.Now().Add(-time.Hour),
			},
			{
				Name: "node-c", Status: "revoked",
			},
		},
	}

	out := m.View()

	for _, want := range []string{
		"omenight relay", "0.0.0.0:2223", "SHA256:abcdef",
		"node-a", "active", "yes",
		"node-b", "pending", "no",
		"node-c", "revoked",
		"6.6 KB", "2.0 MB",
		"127.0.0.1:1085",
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
