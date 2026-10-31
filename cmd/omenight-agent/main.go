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
		return "/etc/omenight"
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

  run     -config /etc/omenight/agent.json
  run     -relay HOST:PORT -name NAME -forward 127.0.0.1:1085=127.0.0.1:1080
  pubkey  -key %s/identity
`, configDir())
	os.Exit(2)
}

func run(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "", "JSON config file (flags override it)")
	relayAddr := fs.String("relay", "", "relay address, host:port")
	name := fs.String("name", "", "client name (the SSH username)")
	keyPath := fs.String("key", "", "identity file")
	knownHosts := fs.String("known-hosts", "", "pinned relay host key")
	socksAddr := fs.String("socks", "", "run the built-in SOCKS5 server on this address")
	verbose := fs.Bool("v", false, "log every connection")
	var forwards stringList
	fs.Var(&forwards, "forward", "listen=target, repeatable")
	fs.Parse(args)

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	var cfg agent.Config
	if *cfgPath != "" {
		c, err := agent.LoadConfig(*cfgPath)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		cfg = c
	}

	if set["relay"] {
		cfg.Relay = *relayAddr
	}
	if set["name"] {
		cfg.Name = *name
	}
	if set["key"] {
		cfg.KeyPath = *keyPath
	}
	if set["known-hosts"] {
		cfg.KnownHosts = *knownHosts
	}
	if set["socks"] {
		cfg.Socks = *socksAddr
	}
	if set["v"] {
		cfg.Verbose = *verbose
	}
	if set["forward"] {
		cfg.Tunnels = nil
		for _, f := range forwards {
			l, t, ok := strings.Cut(f, "=")
			if !ok {
				log.Fatalf("-forward %q: want listen=target", f)
			}
			cfg.Tunnels = append(cfg.Tunnels, agent.Tunnel{Listen: l, Target: t})
		}
	}

	if cfg.Relay == "" || cfg.Name == "" {
		log.Fatal("-relay and -name are required (or give -config)")
	}
	if cfg.KeyPath == "" {
		cfg.KeyPath = filepath.Join(configDir(), "identity")
	}
	if cfg.KnownHosts == "" {
		cfg.KnownHosts = filepath.Join(configDir(), "known_hosts")
	}
	if len(cfg.Tunnels) == 0 && cfg.Socks == "" {
		log.Fatal("nothing to do: give -forward or -socks")
	}

	if cfg.Socks != "" {
		srv, err := socks.Listen(cfg.Socks, cfg.Verbose)
		if err != nil {
			log.Fatalf("socks: %v", err)
		}
		log.Printf("socks5 on %s", srv.Addr())
		go srv.Serve()
	}

	a, err := agent.New(cfg)
	if err != nil {
		log.Fatalf("identity: %v", err)
	}
	log.Printf("identity %s", shortKey(a.PublicKey()))

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

func shortKey(pub string) string {
	f := strings.Fields(pub)
	if len(f) < 2 {
		return pub
	}
	return f[1][:16] + "..."
}
