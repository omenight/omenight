# omenight

Reverse tunnels over SSH, for using small always-on boxes as egress nodes.
A light take on frp: the relay is a control plane, the data plane is stock
SSH channels.

```
agent (node)                          relay
  ├ ssh client ─────── handshake ────▶ ssh server
  │                   pubkey fp → client record
  ├ control channel ──── hello ─────▶ {version, hostname, wanted tunnels}
  │◀──────────────────── config ───── {granted, heartbeat}
  ├ tcpip-forward ──────────────────▶ bind 127.0.0.1:1085
  ├ built-in SOCKS5 on 127.0.0.1:1080
  │◀═══ forwarded-tcpip ══════════════ relay accepts → channel
  └ dial local target, splice ──────▶ clients at relay:1085
```

The relay never learns destinations: it only knows which listen address was
granted. The SOCKS server lives on the node, so name resolution and dialing
happen there. Traffic exits from whatever network the node sits on.

## Install a node

One static binary, no runtime dependencies, works on any distribution with a
POSIX shell. The script detects architecture and init system (systemd,
OpenRC, runit), creates a service user, writes the config, and starts the
service.

```sh
curl -fsSL https://raw.githubusercontent.com/omenight/omenight/main/install.sh \
  | sudo sh -s -- --relay relay.example.com:2223 --name node-a
```

Two values and nothing else. The name defaults to the machine's hostname, so
`--relay` alone is enough; everything else is chosen at runtime. No curl on
the box? Use `wget -qO- <url> | sudo sh -s -- ...`.

It prints the node's public key. Approve it on the relay:

```sh
omenight-relay approve -name node-a
```

Approving also assigns a stable listen port (`20000`, `20001`, ...), shown by
`omenight-relay clients` and the TUI. The agent runs SOCKS5 on a random
loopback port and forwards the assigned port to it, so there is no port
bookkeeping anywhere: one relay address, one name, per node.

Then, from the relay:
`curl --socks5-hostname 127.0.0.1:20000 https://ifconfig.me/ip`
(`socks5h` so DNS resolves on the node).

For anything other than an egress node, be explicit: `--socks
127.0.0.1:1080 --forward 127.0.0.1:1085=127.0.0.1:1080` (repeatable).

Re-running the installer upgrades the binary and keeps the identity. `--from
./omenight-agent` installs a local build, `--no-start` just writes the unit,
`--uninstall` removes the service, `--purge` also removes `/etc/omenight`.

## Run the relay

```sh
go build -o omenight-relay ./cmd/omenight-relay
./omenight-relay serve -addr 0.0.0.0:2223 -state state.json -hostkey hostkey
```

A systemd unit is in `deploy/omenight-relay.service`.

```sh
omenight-relay clients                              # enrolled nodes and ports
omenight-relay approve  -name node-a                # activates + assigns a port
omenight-relay allow    -name node-a -addr 127.0.0.1:2100   # extra listen addr
omenight-relay revoke   -name node-a                # also kills live sessions
```

Approvals take effect on the running daemon within two seconds: it reloads
the state file and drops sessions whose grants no longer match, so the agent
reconnects and renegotiates.

## TUI

```sh
omenight-relay tui
```

Live view over the daemon's unix socket: name, status, online, connections,
bytes each way, assigned port, last seen. `j`/`k` move, `a` approves the
selected node (which also assigns its port), `x` revokes it, `r` refreshes,
`q` quits. Actions are sent to the running daemon, so they apply immediately —
a revoke unbinds the port and drops the session while you watch.

The socket also answers `curl --unix-socket /run/omenight/control.sock http://x/`
with JSON.

## Measured

Node: Alpine 3.24, aarch64, 4 cores, 973 MB RAM, residential link.
Relay: Debian 13, x86_64.

| | |
|---|---|
| agent binary, arm64, stripped | 4.9 MB |
| idle RSS | ~6 MB |
| sustained throughput, one SSH channel | ~93 Mbps |
| added TTFB for a page fetch | +250 ms |
| half-open detection | 30 s, then the port is unbound |

Latency, not bandwidth, is the cost of this design: every origin round trip
includes the node's RTT.

## Security model

- Ed25519 keys only, no passwords. The fingerprint is the identity; the SSH
  username is a label. Revocation is enforced at auth and on live sessions.
- Grants are per session: a valid key cannot bind a port it did not negotiate
  in its `hello`, even if it is in the allowlist.
- Listeners bind to what you configure. Use `127.0.0.1:PORT` and reach it over
  your own SSH session or WireGuard — a public SOCKS port is an open proxy.
- The node's SOCKS port reaches that node's whole LAN at TCP level. Treat it
  like a root password.
- The agent pins the relay's host key at first use and refuses changes.

## Scope

TCP only. SSH has no datagrams, so no UDP, no QUIC, no WireGuard-over-this.
Browsers fall back to TCP; video works, just without HTTP/3.

Not there yet: config push without a reconnect, `disallow`, load balancing,
per-tunnel byte persistence, UDP.

## Build

```sh
make dist   # agent for arm64, relay for amd64
make test
```

Static, `CGO_ENABLED=0`, no cgo, no database. State is a JSON file with atomic
writes.
