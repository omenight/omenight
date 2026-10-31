# AGENTS.md

omenight: reverse TCP tunnels over SSH. A node agent dials out to a relay; the
relay is an SSH server with a control plane. Two binaries from one Go module
named `omenight` (no domain in the module path).

## Commands

```sh
gofmt -w .            # CI rejects any unformatted file
go vet ./...
go test ./...
go test ./internal/store -run TestEnrollmentFlow   # single test

make agent            # bin/omenight-agent
make relay            # bin/omenight-relay
make dist             # arm64 agent + amd64 relay into dist/
```

Always build with `CGO_ENABLED=0`: the node runs Alpine/musl and the binary
must be static.

Pushing a `v*` tag builds and publishes release assets; `install.sh` always
downloads from `releases/latest`. A change to the agent is not shipped until a
tag is pushed.

## Verifying the tunnel path

There is no integration test. Exercise it by hand:

```sh
./bin/omenight-relay serve -addr 127.0.0.1:2222 -state /tmp/s.json \
  -hostkey /tmp/hk -socket /tmp/c.sock
./bin/omenight-agent run -relay 127.0.0.1:2222 -name node -egress \
  -key /tmp/id -known-hosts /tmp/kh
./bin/omenight-relay approve -state /tmp/s.json -name node   # prints the port
curl --socks5-hostname 127.0.0.1:20000 https://ifconfig.me/ip
```

The first connect is `pending` and binds nothing; that is the enrollment path,
not a failure. Suffix `socks5h`/`--socks5-hostname` matters: DNS must resolve on
the node.

Start background processes with `setsid cmd </dev/null >log 2>&1 &`. Plain
`nohup` gets killed with the parent shell in this environment.

## Architecture facts that are not visible in filenames

- The data plane is stock SSH. The relay implements `tcpip-forward` and
  `forwarded-tcpip` by hand because `x/crypto/ssh` has no server-side `Listen`.
  Do not invent a custom channel type; the agent never needs `ssh.Client.Listen`.
- Grants are negotiated per session, and the allowlist is the policy. In
  `hello` the agent either names exact listen addresses or sends none, which
  means "everything I am allowed" — that is egress mode, where the relay owns
  the port number. `handleForward` then checks requests against those session
  grants, not the store, so editing the allowlist mid-session does nothing
  until the session is dropped and renegotiated.
- The relay never learns destinations. Targets live in the agent's config and
  the SOCKS5 server runs on the node; anything that makes the relay dial a
  destination is a design change.
- The store is a JSON file held in memory with atomic saves. `watch()` reloads
  it every 2s and drops sessions whose status or allow list changed, so CLI
  approve/revoke applies live by forcing a reconnect. Do not add a
  "restart to apply" path.
- Liveness couples both sides: the agent pings the control channel every
  `HeartbeatSec` (15), the relay watchdog drops at 2× that and unbinds the
  ports. Changing one interval without the other breaks detection.
- `approve` does two things: sets the status and, if the client has no allow
  entry yet, allocates the lowest free port from 20000 up and writes it into
  the allow list. The port is therefore persisted per client and stable across
  reconnects; it is never reassigned while the client exists.
- The agent merges a JSON config file with flags; flags win, tracked via
  `fs.Visit`. The installer writes that file so every init system shares one
  source of truth.

## Traps

- Release asset names are a contract: `install.sh` builds
  `$SVC-linux-$ARCH` = `omenight-agent-linux-<amd64|arm64|armv6|armv7>`.
  `Makefile:dist` and `release.yml` must match or installs 404 silently.
- `install.sh` defaults to an egress node (`"egress": true`); `--socks` and
  `--forward` switch it to manual mode. It runs under POSIX sh on Alpine: no
  bashisms, no curl assumption (wget fallback), busybox `adduser` differs from
  `useradd`. Test it with `--from ./binary --no-start`.
- Anchoring `.gitignore` patterns is not cosmetic. A bare `identity` pattern
  ignored `internal/identity/` and broke a release. Use `/identity`.
- The relay binary carries the TUI (bubbletea). Keep TUI dependencies out of the
  agent — that is the binary shipped to small boxes.
- TCP only. SSH has no datagrams, so UDP and QUIC are out of scope, not missing
  features to add.
