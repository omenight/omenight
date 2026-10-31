package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"omenight/internal/relay"
	"omenight/internal/store"
	"omenight/internal/tui"
)

const (
	defState    = "relay-state.json"
	defHostKey  = "relay_hostkey"
	defAddr     = "0.0.0.0:2223"
	defSocket   = "/run/omenight/control.sock"
	defPortBase = 20000
)

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "serve":
		serve(args)
	case "tui":
		runTUI(args)
	case "clients":
		clients(args)
	case "approve":
		approve(args)
	case "revoke":
		setStatus(args, store.StatusRevoked)
	case "allow":
		allow(args)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `omenight-relay - control plane for omenight agents

  serve    -addr %s -state %s -hostkey %s -socket %s
  tui      -socket %s
  clients  -state %s
  approve  -name NAME -state %s        (also grants a stable listen port)
  revoke   -name NAME -state %s
  allow    -name NAME -addr 127.0.0.1:1085 -state %s
`, defAddr, defState, defHostKey, defSocket, defSocket, defState, defState, defState, defState)
	os.Exit(2)
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", defAddr, "listen address")
	state := fs.String("state", defState, "state file")
	hostkey := fs.String("hostkey", defHostKey, "host key path")
	socket := fs.String("socket", defSocket, "unix socket for the TUI ('' to disable)")
	fs.Parse(args)

	r, err := relay.New(relay.Config{Addr: *addr, State: *state, HostKey: *hostkey, Socket: *socket})
	if err != nil {
		log.Fatal(err)
	}
	if err := r.Serve(); err != nil {
		log.Fatal(err)
	}
}

func runTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	socket := fs.String("socket", defSocket, "daemon socket")
	fs.Parse(args)

	p := tea.NewProgram(tui.New(*socket), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatal(err)
	}
}

func clients(args []string) {
	fs := flag.NewFlagSet("clients", flag.ExitOnError)
	state := fs.String("state", defState, "state file")
	fs.Parse(args)

	st, err := store.Open(*state)
	if err != nil {
		log.Fatal(err)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tALLOW\tLAST SEEN")
	for _, c := range st.All() {
		seen := "-"
		if !c.LastSeen.IsZero() {
			seen = time.Since(c.LastSeen).Round(time.Second).String() + " ago"
		}
		allow := fmt.Sprint(c.Allow)
		if len(c.Allow) == 0 {
			allow = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Name, c.Status, allow, seen)
	}
	w.Flush()
}

// approve activates a client and, if it has no listen address yet, gives it
// the lowest free port from the relay's allocation range. The port is
// persisted, so it survives reconnects and restarts.
func approve(args []string) {
	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	state := fs.String("state", defState, "state file")
	name := fs.String("name", "", "client name")
	base := fs.Int("port-base", defPortBase, "first port to allocate")
	fs.Parse(args)
	if *name == "" {
		log.Fatal("-name is required")
	}

	st, err := store.Open(*state)
	if err != nil {
		log.Fatal(err)
	}
	c, ok := st.ByName(*name)
	if !ok {
		log.Fatalf("%s: no such client (has it connected yet?)", *name)
	}
	if len(c.Allow) == 0 {
		addr, err := allocate(st, *base)
		if err != nil {
			log.Fatal(err)
		}
		if c, err = st.Allow(*name, addr); err != nil {
			log.Fatal(err)
		}
	}
	if c, err = st.SetStatus(*name, store.StatusActive); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s approved, may bind %v\n", c.Name, c.Allow)
}

// allocate finds a port nobody else is using and that can actually be bound.
func allocate(st *store.Store, base int) (string, error) {
	used := map[int]bool{}
	for _, c := range st.All() {
		for _, a := range c.Allow {
			if host, port, err := net.SplitHostPort(a); err == nil && host == "127.0.0.1" {
				if n, err := strconv.Atoi(port); err == nil {
					used[n] = true
				}
			}
		}
	}
	for i := 0; i < 64; i++ {
		p := store.NextFreePort(base, 1000, used)
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

func setStatus(args []string, status string) {
	fs := flag.NewFlagSet("setstatus", flag.ExitOnError)
	state := fs.String("state", defState, "state file")
	name := fs.String("name", "", "client name")
	fs.Parse(args)
	if *name == "" {
		log.Fatal("-name is required")
	}
	st, err := store.Open(*state)
	if err != nil {
		log.Fatal(err)
	}
	c, err := st.SetStatus(*name, status)
	if err != nil {
		log.Fatalf("%s: %v", *name, err)
	}
	fmt.Printf("%s is now %s\n", c.Name, c.Status)
}

func allow(args []string) {
	fs := flag.NewFlagSet("allow", flag.ExitOnError)
	state := fs.String("state", defState, "state file")
	name := fs.String("name", "", "client name")
	addr := fs.String("addr", "", "listen address on the relay, e.g. 127.0.0.1:1085")
	fs.Parse(args)
	if *name == "" || *addr == "" {
		log.Fatal("-name and -addr are required")
	}
	st, err := store.Open(*state)
	if err != nil {
		log.Fatal(err)
	}
	c, err := st.Allow(*name, *addr)
	if err != nil {
		log.Fatalf("%s: %v", *name, err)
	}
	fmt.Printf("%s may bind %v\n", c.Name, c.Allow)
}
