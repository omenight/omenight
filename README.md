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
  └ dial local target, splice ──────▶ your browser at relay:1085
```

The relay never learns the destination: it only knows which listen address
was granted. The SOCKS server lives on the node, so name resolution and
dialing happen there.

## Status

Working: enrollment by key, admin approval, per-client port allowlist,
reverse TCP forwarding, heartbeat with half-open detection, reconnect with
backoff, live policy reload.

Not there yet: UDP (SSH has none), config push without a reconnect, byte
accounting per tunnel, load balancing, the `disallow` subcommand.

## Measured on real hardware

Node: Alpine 3.24, aarch64, 4 cores, 973 MB RAM, Sochi home link.
Relay: Debian 13, x86_64.

| | |
|---|---|
| agent binary, arm64, stripped | 4.9 MB |
| idle RSS | ~6 MB |
| sustained throughput, one SSH channel | ~93 Mbps |
| added TTFB for a page fetch | +250 ms |
| half-open detection | 30 s, then the port is unbound |

Latency, not bandwidth, is the cost of this design: every origin round trip
now includes the node's home RTT.

## Quickstart

Relay:

```sh
go build -o bin/omenight-relay ./cmd/omenight-relay
./bin/omenight-relay serve -addr 0.0.0.0:2223 -state state.json -hostkey hostkey
```

Node:

```sh
go build -o bin/omenight-agent ./cmd/omenight-agent
./bin/omenight-agent run \
  -relay relay.example.com:2223 -name pi-sochi \
  -socks 127.0.0.1:1080 \
  -forward 127.0.0.1:1085=127.0.0.1:1080
```

The first connection shows up as `pending`. Approve it:

```sh
./omenight-relay clients   -state state.json
./omenight-relay approve   -state state.json -name pi-sochi
./omenight-relay allow     -state state.json -name pi-sochi -addr 127.0.0.1:1085
./omenight-relay revoke    -state state.json -name pi-sochi
```

A live approval drops the session; the agent reconnects within a second and
picks up the new grants. Revocation does the same and keeps the key out.

Then, from the relay:

```sh
curl --socks5-hostname 127.0.0.1:1085 https://ifconfig.me/ip
```

Use `socks5h` so DNS resolves on the node.

## Enroll a node

The SSH username is the client name. The key is the identity, and the
fingerprint is what gets pinned:

```sh
./omenight-agent pubkey -key ~/.config/omenight/identity
```

## Security model

- Ed25519 keys only, no passwords. Revocation is a status flag, enforced at
  auth and on live sessions.
- Every listen address must be in the client's `allow` list, and the grant is
  per session: a valid key cannot bind a port it did not negotiate.
- Listeners bind as configured. Use `127.0.0.1:PORT` and reach it over your
  own SSH session or WireGuard — a public SOCKS port is an open proxy.
- The node's SOCKS port reaches the node's whole LAN at TCP level. Treat it
  like a root password.
- The relay pins its host key on the agent at first use and refuses changes.

## Deploy

The node in this setup runs Alpine, so it needs OpenRC, not systemd:

```sh
install -m 0755 dist/omenight-agent-arm64 /usr/local/bin/omenight-agent
install -m 0755 deploy/omenight-agent.openrc /etc/init.d/omenight-agent
install -m 0640 deploy/omenight-agent.confd /etc/conf.d/omenight-agent
rc-update add omenight-agent default
rc-service omenight-agent start
```

The relay on Debian: `deploy/omenight-relay.service`.

## Build

```sh
make dist    # arm64 node + amd64 relay, static, stripped
```

No cgo, no runtime dependencies on the node: one static binary plus an
identity file.
