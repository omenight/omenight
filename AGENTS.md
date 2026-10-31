# AGENTS.md

omenight: WireGuard exit nodes behind one relay, AmneziaWG optional. One Go binary,
`omenight-relay`, plus `install.sh`, which runs on the nodes. Module name
`omenight` (no domain in the module path).

## Commands

```sh
gofmt -w .            # CI rejects any unformatted file
go vet ./...
go test ./...
go test ./internal/wg -run TestSync   # single test
make relay            # bin/omenight-relay
make e2e              # real kernel in user namespaces, needs wg, wg-quick, nft, python3
```

Always build with `CGO_ENABLED=0`, the relay binary must be static.

## How it fits together

- `init` fixes the settings (endpoint, net, port/mark base, iface prefix,
  relay key, optional AWG params). Node N gets interface `om<N>`, UDP `51820+N`, the
  N-th /30 of the net (relay `.1`, node `.2`) and fwmark = routing table
  `7700+N`. Everything derives from the slot, so settings never change after
  `init`. `Store.Init` refuses a second run.
- `add` generates the node's key pair on the relay and prints a paste block,
  `sh <<'OMENIGHT'`, the node's wg-quick conf in `OMENIGHT_CONF`, then
  `install.sh` from `go:embed` with comments and blank lines stripped. The
  installer version is pinned to the relay binary, nothing is downloaded.
- The daemon (`serve`) only reconciles. `wg.Manager.Sync` shells out to `ip`,
  `awg`/`wg` and `sysctl` and is idempotent. It runs when the state file's
  mtime changes (2s poll), after a socket command, and every 30s to heal drift.
- The state file is the only source of truth. CLI commands write it directly,
  the daemon picks it up. Socket commands call `sync()` themselves, because
  the mtime watch cannot see the daemon's own writes.
- Online status comes from the kernel's latest handshake (under 3 min), not
  from the store. Nothing liveness-related is persisted.

## Facts that are not visible in the code

- OpenWrt busybox has no `base64`. That is why the paste block is plain text.
  Do not "optimize" it into a gzip/base64 blob.
- `install.sh` is wrapped in `main "$@" </dev/null` on purpose. It is read
  from stdin when pasted, so `sh` must parse the whole script before any
  command runs, or a child reading stdin would eat the rest of the script.
- The paste delimiter `OMENIGHT` must never be a line of `install.sh`, and
  lines starting with `#` vanish in the paste, so no heredoc in the installer
  may rely on them. `TestPaste` checks the delimiter.
- A WireGuard interface picks the peer by destination, so two exit nodes
  cannot share one interface with `0.0.0.0/0`. Hence one interface per node.
- Revoked nodes keep their interface and routes without a peer.
  Marked traffic is then dropped, not routed out of the relay's own IP. Same
  reason for the `unreachable default` IPv6 route in every node table.
- rp_filter is set to 2 per interface. Replies arrive on `omN` from sources
  the main table routes elsewhere, and the kernel uses max(all, iface). Loose
  mode still needs the source routable via the main table, so the relay host
  must have a default route (every VPS does, the e2e test fakes one).
- Revoke removes the peer, so the relay forgets the node's endpoint. After
  approve, traffic flows again only with the node's next keepalive (25s).
- Ip rules owned by the relay all sit at priority `wg.RulePref` (7700). Sync
  deletes any rule at that priority it does not want, and touches no other.
  Interfaces are ours only if named prefix + digits.
- After a reboot, until the daemon starts, there are no rules, so marked
  traffic leaves from the relay itself. Start the relay before Xray if that
  matters.
- The node conf has `Table = off` and `AllowedIPs = <relay>/32`. The node's
  own traffic never enters the tunnel. The node masquerades out its WAN and
  blocks private ranges from the tunnel. On OpenWrt that is a fw4 zone with
  forwarding only to `wan` (the `wan` zone's masq does the NAT). pbr with
  `lan_device br-lan` does not touch tunnel traffic.
- Plain WireGuard is the default because AWG has no packaged kernel module on
  Debian. Its DKMS build needs the full kernel source and can break on kernel
  upgrades, and userspace amneziawg-go would mean a process per node. AWG
  params, if used, are the 1.0 set (jc, jmin, jmax, s1, s2, h1-h4).
- The private key goes to `wg set` (or `awg set`) through stdin (`private-key /dev/stdin`)
  so it never shows in `ps`. `TestSync` enforces that.

## Traps

- `install-relay.sh` holds the only copy of the systemd unit. It downloads
  `omenight-relay-linux-<arch>` and `SHA256SUMS` from the release, so those
  asset names are a contract with `release.yml`.
- netifd loads protocol handlers only at startup and needs `ubus call network
  reload` before `ifup` sees a new interface. The installer asks
  `get_proto_handlers` and restarts the network only when the handler is not
  loaded. Stubbed dry runs cannot catch either, test on a real router.
- `install.sh` runs under busybox ash. No bashisms, no `grep -A`, no
  `base64`. Test changes there (`ssh <router> sh < script`) with `uci`,
  `ifup` and the firewall reload stubbed out.
- Anchoring `.gitignore` patterns is not cosmetic. A bare pattern can match a
  package directory under `internal/` and silently drop it from a release.
- TUI dependencies (bubbletea) are fine in the relay. There is no node binary
  any more, the node side is only `install.sh`.
