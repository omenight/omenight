# omenight

Exit nodes behind one relay, over WireGuard (AmneziaWG optional). The relay
gives every node its own interface and fwmark, so Xray (or anything that can
mark a socket) picks the node a client leaves through. The node only forwards
the relay's traffic out of its WAN, its own routing stays as it was.

```
xray outbound, mark 7701 ──▶ relay om1 ══wg══▶ node ──masquerade──▶ internet
```

## Relay

```sh
curl -fsSL https://github.com/omenight/omenight/releases/latest/download/install-relay.sh \
  | sudo sh -s -- --endpoint home.omenight.com    # --kind awg for AmneziaWG

omenight-relay add -name archer-ax23              # prints the block to paste on the node
omenight-relay tui                                # live view, a/x approve/revoke
```

The installer adds `wireguard-tools`, checks the binary against SHA256SUMS,
runs `init` once and starts the systemd service. Run it again to upgrade,
`--uninstall` (and `--purge`) to remove.

`add` prints the Xray outbound too. A node can be `revoke`d (peer dropped,
marked traffic blackholed) and `approve`d back, or `remove`d (slot freed, an
uninstall block printed). `show -name N` prints the block again, `-conf` the
plain wg-quick file, `-xray` the outbound.

Needs `wg` from wireguard-tools, the kernel module is in every distro.
`-kind awg` needs the AmneziaWG module and `awg` instead, which most distro
kernels do not ship. Runs as root with only `CAP_NET_ADMIN`.

## Node

Paste the block from `add` into a root shell. It carries the node's config
and the installer, needs nothing but `sh`, and is safe to paste again.

- OpenWrt: uci interface, a firewall zone that may only forward to `wan`,
  `kmod-wireguard` and `wireguard-tools` via apk/opkg if missing.
- Other Linux: `wg-quick@omenight` (systemd) with nftables rules for
  masquerade, LAN block and ssh-only input.

## Notes

- IPv4 only. Marked IPv6 is blackholed on the relay instead of leaking.
- One UDP port per node (`51820+N`). Open them if the relay has a firewall.
- Ports, addresses and marks derive from settings fixed at `init`.

## Build

```sh
make relay   # bin/omenight-relay
make test
make e2e     # namespaces, real kernel, no root
```
