package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"

	"omenight/internal/admin"
	"omenight/internal/relay"
	"omenight/internal/store"
	"omenight/internal/tui"
	"omenight/internal/wg"
)

const (
	defState  = "/var/lib/omenight/state.json"
	defSocket = "/run/omenight/control.sock"
)

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "init":
		initRelay(args)
	case "serve":
		serve(args)
	case "tui":
		runTUI(args)
	case "clients":
		clients(args)
	case "add":
		add(args)
	case "show":
		show(args)
	case "approve":
		setStatus(args, (*admin.Admin).Approve, "active")
	case "revoke":
		setStatus(args, (*admin.Admin).Revoke, "revoked, its traffic is dropped")
	case "remove":
		remove(args)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `omenight-relay - AmneziaWG exit nodes behind one relay

  init     -endpoint HOST [-kind awg|wg] [-net %s] [-port-base %d] [-mark-base %d]
  serve    [-socket %s]
  add      -name NAME            print the block to paste on the node
  show     -name NAME [-conf | -xray]
  approve  -name NAME            re-enable a revoked node
  revoke   -name NAME            drop its peer, keep its slot
  remove   -name NAME            free its slot, print the uninstall block
  clients
  tui      [-socket %s]

Every command takes -state (default %s).
`, admin.DefaultNet, admin.DefaultPortBase, admin.DefaultMarkBase, defSocket, defSocket, defState)
	os.Exit(2)
}

func flags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	return fs, fs.String("state", defState, "state file")
}

func open(path string) (*store.Store, store.Settings) {
	st, err := store.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	set, err := st.Settings()
	if err != nil {
		log.Fatal(err)
	}
	return st, set
}

func client(st *store.Store, name string) store.Client {
	if name == "" {
		log.Fatal("-name is required")
	}
	c, ok := st.ByName(name)
	if !ok {
		log.Fatalf("%s: no such client", name)
	}
	return c
}

func initRelay(args []string) {
	fs, state := flags("init")
	endpoint := fs.String("endpoint", "", "hostname or IP nodes dial, e.g. home.omenight.com")
	kind := fs.String("kind", wg.KindAWG, "awg (AmneziaWG) or wg (plain WireGuard)")
	nw := fs.String("net", admin.DefaultNet, "tunnel addresses, one /30 per node")
	portBase := fs.Int("port-base", admin.DefaultPortBase, "node N listens on UDP port-base+N")
	markBase := fs.Int("mark-base", admin.DefaultMarkBase, "node N gets fwmark and table mark-base+N")
	prefix := fs.String("prefix", admin.DefaultPrefix, "node N's interface is prefix+N")
	fs.Parse(args)

	st, err := store.Open(*state)
	if err != nil {
		log.Fatal(err)
	}
	set, err := admin.New(st).Init(store.Settings{
		Kind: *kind, Endpoint: *endpoint, Net: *nw,
		PortBase: *portBase, MarkBase: *markBase, Prefix: *prefix,
	})
	if err != nil {
		log.Fatal(err)
	}
	pub, _ := wg.PublicKey(set.PrivateKey)
	fmt.Printf("initialized %s\n  %s on %s, net %s, ports %d+, marks %d+\n  public key %s\n",
		*state, set.Kind, set.Endpoint, set.Net, set.PortBase+1, set.MarkBase+1, pub)
}

func serve(args []string) {
	fs, state := flags("serve")
	socket := fs.String("socket", defSocket, "unix socket for the TUI ('' to disable)")
	fs.Parse(args)

	r, err := relay.New(relay.Config{State: *state, Socket: *socket})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := r.Serve(ctx); err != nil {
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
	fs, state := flags("clients")
	fs.Parse(args)

	st, set := open(*state)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tIFACE\tPORT\tMARK\tNODE")
	for _, c := range st.All() {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n", c.Name, c.Status,
			set.Iface(c.Slot), set.Port(c.Slot), set.Mark(c.Slot), set.NodeAddr(c.Slot).Addr())
	}
	w.Flush()
}

// add and show write the paste block alone to stdout, so `> node.sh` works.
func add(args []string) {
	fs, state := flags("add")
	name := fs.String("name", "", "node name")
	fs.Parse(args)
	if *name == "" {
		log.Fatal("-name is required")
	}

	st, set := open(*state)
	c, err := admin.New(st).Add(*name)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "%s added: %s, udp %d, node %s, mark %d\n\npaste this into a root shell on the node:\n\n",
		c.Name, set.Iface(c.Slot), set.Port(c.Slot), set.NodeAddr(c.Slot).Addr(), set.Mark(c.Slot))
	printPaste(set, c)
	fmt.Fprintf(os.Stderr, "\nXray outbound for this node:\n%s\n", admin.Xray(set, c))
}

func show(args []string) {
	fs, state := flags("show")
	name := fs.String("name", "", "node name")
	conf := fs.Bool("conf", false, "print the awg-quick config instead")
	xray := fs.Bool("xray", false, "print the Xray outbound instead")
	fs.Parse(args)

	st, set := open(*state)
	c := client(st, *name)
	switch {
	case *conf:
		s, err := admin.Conf(set, c)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(s)
	case *xray:
		fmt.Println(admin.Xray(set, c))
	default:
		printPaste(set, c)
	}
}

func printPaste(set store.Settings, c store.Client) {
	s, err := admin.Paste(set, c)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(s)
}

func setStatus(args []string, op func(*admin.Admin, string) (store.Client, error), done string) {
	fs, state := flags("status")
	name := fs.String("name", "", "node name")
	fs.Parse(args)

	st, _ := open(*state)
	c, err := op(admin.New(st), client(st, *name).Name)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s %s\n", c.Name, done)
}

func remove(args []string) {
	fs, state := flags("remove")
	name := fs.String("name", "", "node name")
	fs.Parse(args)

	st, _ := open(*state)
	c, err := admin.New(st).Remove(client(st, *name).Name)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "%s removed, its interface goes away within 2s\n\nto clean up the node, paste this into a root shell there:\n\n", c.Name)
	fmt.Print(admin.UninstallPaste())
}
