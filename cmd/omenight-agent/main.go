package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"omenight/internal/agent"
	"omenight/internal/socks"
)

func configDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ".omenight"
	}
	return filepath.Join(d, "omenight")
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		run(os.Args[2:])
	case "pubkey":
		pubkey(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `omenight-agent - reverse tunnel node

  run     -relay HOST:PORT -name NAME -forward 127.0.0.1:1081=127.0.0.1:1080
  pubkey  -key %s/identity
`, configDir())
	os.Exit(2)
}

func run(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	relayAddr := fs.String("relay", "", "relay address, host:port")
	name := fs.String("name", "", "client name (the SSH username)")
	keyPath := fs.String("key", filepath.Join(configDir(), "identity"), "identity file")
	knownHosts := fs.String("known-hosts", filepath.Join(configDir(), "known_hosts"), "pinned relay host key")
	socksAddr := fs.String("socks", "", "run the built-in SOCKS5 server on this address")
	verbose := fs.Bool("v", false, "log every connection")
	var forwards stringList
	fs.Var(&forwards, "forward", "listen=target, repeatable")
	fs.Parse(args)

	if *relayAddr == "" || *name == "" {
		log.Fatal("-relay and -name are required")
	}
	if len(forwards) == 0 && *socksAddr == "" {
		log.Fatal("nothing to do: give -forward or -socks")
	}

	if *socksAddr != "" {
		srv, err := socks.Listen(*socksAddr, *verbose)
		if err != nil {
			log.Fatalf("socks: %v", err)
		}
		log.Printf("socks5 on %s", srv.Addr())
		go srv.Serve()
	}

	cfg := agent.Config{
		Name:       *name,
		Relay:      *relayAddr,
		KeyPath:    *keyPath,
		KnownHosts: *knownHosts,
		Verbose:    *verbose,
	}
	for _, f := range forwards {
		l, t, ok := strings.Cut(f, "=")
		if !ok {
			log.Fatalf("-forward %q: want listen=target", f)
		}
		cfg.Tunnels = append(cfg.Tunnels, agent.Tunnel{Listen: l, Target: t})
	}

	a, err := agent.New(cfg)
	if err != nil {
		log.Fatalf("identity: %v", err)
	}
	log.Printf("identity %s", strings.SplitN(a.PublicKey(), " ", 3)[1][:16]+"...")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil {
		log.Fatal(err)
	}
}

func pubkey(args []string) {
	fs := flag.NewFlagSet("pubkey", flag.ExitOnError)
	keyPath := fs.String("key", filepath.Join(configDir(), "identity"), "identity file")
	fs.Parse(args)

	a, err := agent.New(agent.Config{KeyPath: *keyPath})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(a.PublicKey())
}
