# omenight

Reverse tunnels over SSH, for using small always-on boxes as egress nodes.
A light take on frp: the relay is a control plane, the data plane is stock SSH
channels.

```
agent (node)                        relay
  ├ ssh client ───── handshake ────▶ ssh server
  ├ control channel ──── hello ────▶ {version, hostname}
  │◀─────────────────── config ───── {granted ports, heartbeat}
  ├ tcpip-forward ─────────────────▶ bind 127.0.0.1:20000
  ├ SOCKS5 on a random loopback port
  │◀═══ forwarded-tcpip ═════════════ relay accepts → channel
  └ dial local target, splice ─────▶ clients at relay:20000
```

The relay never learns destinations: the SOCKS server runs on the node, so
dialing and DNS happen there. Traffic exits from the node's network.

## Install a node

```sh
curl -fsSL https://raw.githubusercontent.com/omenight/omenight/main/install.sh \
  | sudo sh -s -- --relay relay.example.com:2223 --name node-a
```

One static binary, no runtime dependencies, any distro with a POSIX shell.
Detects architecture and init system (systemd, OpenRC, runit), creates a
service, starts it, prints the node's key. `--name` defaults to the hostname;
no curl on the box? `wget -qO- <url> | sudo sh -s -- ...`.

Approve it on the relay and use it:

```sh
omenight-relay approve -name node-a      # assigns 127.0.0.1:20000
curl --socks5-hostname 127.0.0.1:20000 https://ifconfig.me/ip
```

No port bookkeeping: the relay assigns a stable port per node (`20000`,
`20001`, …) and the node picks its own loopback SOCKS port.

Re-running upgrades in place and keeps the identity. `--from ./omenight-agent`
installs a local build, `--no-start` only writes the unit, `--uninstall`,
`--purge`.

## Run the relay

```sh
go build -o omenight-relay ./cmd/omenight-relay
./omenight-relay serve -addr 0.0.0.0:2223 -state state.json -hostkey hostkey
```

```sh
omenight-relay clients                       # enrolled nodes and ports
omenight-relay approve -name node-a          # activate + assign a port
omenight-relay revoke  -name node-a          # also drops the live session
omenight-relay allow   -name node-a -addr 127.0.0.1:2100
```

A systemd unit is in `deploy/omenight-relay.service`.

## TUI

```sh
omenight-relay tui
```

Live state, connections, byte counters and assigned ports. `j`/`k` move,
`a` approve, `x` revoke, `r` refresh, `q` quit. Actions go to the running
daemon, so they apply immediately.

The daemon's socket also answers
`curl --unix-socket /run/omenight/control.sock http://x/` with JSON.

## Measured

Node: Alpine 3.24, aarch64, 4 cores, 973 MB RAM, residential link.
Relay: Debian 13, x86_64.

| | |
|---|---|
| agent binary, arm64, stripped | 4.9 MB |
| idle RSS | ~6 MB |
| throughput, one SSH channel | ~93 Mbps |
| added TTFB for a page fetch | +250 ms |

Latency, not bandwidth, is the cost of this design: every origin round trip
includes the node's RTT.

## Security

- Ed25519 keys only. The fingerprint is the identity; revocation is enforced
  at auth and against live sessions.
- The allow list is the policy: a session can only bind addresses granted to
  it, and it renegotiates when the list changes.
- Listeners are loopback by default. A public SOCKS port is an open proxy.
  The node's SOCKS reaches that node's whole LAN, so treat it like a root
  password.
- The agent pins the relay's host key on first use and refuses changes.

## Scope

TCP only: SSH has no datagrams, so no UDP or QUIC. Browsers fall back to TCP.

```sh
make dist   # arm64 agent + amd64 relay, static
make test
```

Not there yet: config push without a reconnect, `disallow`, load balancing,
per-tunnel byte persistence, UDP.
