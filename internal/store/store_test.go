package store

import (
	"path/filepath"
	"testing"
)

func TestEnrollmentFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	daemon, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	c, err := daemon.Register("pi", "SHA256:abc")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusPending {
		t.Fatalf("new client should be pending, got %s", c.Status)
	}

	// Re-registering the same key must not create a second record.
	if _, err := daemon.Register("pi", "SHA256:abc"); err != nil {
		t.Fatal(err)
	}
	if n := len(daemon.All()); n != 1 {
		t.Fatalf("want 1 client, got %d", n)
	}

	// The CLI is a separate process: it opens the file, mutates, saves.
	cli, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.SetStatus("pi", StatusActive); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Allow("pi", "127.0.0.1:1085"); err != nil {
		t.Fatal(err)
	}

	changed, err := daemon.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("daemon should notice the CLI's write")
	}

	got, ok := daemon.ByFingerprint("SHA256:abc")
	if !ok {
		t.Fatal("client lost")
	}
	if got.Status != StatusActive {
		t.Fatalf("status not reloaded: %s", got.Status)
	}
	if len(got.Allow) != 1 || got.Allow[0] != "127.0.0.1:1085" {
		t.Fatalf("allow not reloaded: %v", got.Allow)
	}

	// Allow is a set, not a list.
	if _, err := cli.Allow("pi", "127.0.0.1:1085"); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.Reload(); err != nil {
		t.Fatal(err)
	}
	got, _ = daemon.ByFingerprint("SHA256:abc")
	if len(got.Allow) != 1 {
		t.Fatalf("duplicate allow entry: %v", got.Allow)
	}
}

func TestNextFreePort(t *testing.T) {
	used := map[int]bool{}
	if got := NextFreePort(20000, 3, used); got != 20000 {
		t.Fatalf("want 20000, got %d", got)
	}
	used[20000] = true
	used[20001] = true
	if got := NextFreePort(20000, 3, used); got != 20002 {
		t.Fatalf("want 20002, got %d", got)
	}
	used[20002] = true
	if got := NextFreePort(20000, 3, used); got != 0 {
		t.Fatalf("want exhaustion (0), got %d", got)
	}
}
