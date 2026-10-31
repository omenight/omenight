# omenight

Reverse TCP tunnels over SSH. A small agent on a box at home dials a relay and
exposes a SOCKS proxy on the relay's loopback, so traffic exits from that box's
network. The relay is a control plane; the data plane is stock SSH channels.

```
node ──ssh──▶ relay:2223     node: SOCKS5 on a random loopback port
                             relay: binds 127.0.0.1:20000 for it
```

## Install a node

```sh
curl -fsSL https://raw.githubusercontent.com/omenight/omenight/main/install.sh \
  | sudo sh -s -- --relay relay.example.com:2223 --name node-a
```

Static binary, no dependencies, detects arch and init system (systemd, OpenRC,
runit). The name defaults to the hostname. `wget -qO- <url> | sudo sh` works too.

## Run the relay

```sh
./omenight-relay serve -addr 0.0.0.0:2223 -state state.json -hostkey hostkey

omenight-relay approve -name node-a    # assigns 127.0.0.1:20000
omenight-relay tui                     # live view, a/x approve/revoke
curl --socks5-hostname 127.0.0.1:20000 https://ifconfig.me/ip
```

Ports are assigned by the relay, so a node only needs an address and a name.

## Notes

- TCP only. SSH has no datagrams, so no UDP or QUIC.
- Listeners bind loopback. A public SOCKS port is an open proxy, and a node's
  SOCKS reaches its whole LAN.
- 4.9 MB static arm64 agent, ~6 MB RSS, ~93 Mbps per tunnel, +250 ms per page.

## Build

```sh
make dist   # arm64 agent, amd64 relay
make test
```
