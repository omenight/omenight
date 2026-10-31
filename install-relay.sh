#!/bin/sh
# omenight relay installer for systemd distros. Re-running it upgrades in place.
# Usage is `sh install-relay.sh --endpoint home.example.com`, see --help.
set -eu

REPO="${OMENIGHT_REPO:-omenight/omenight}"
BIN=/usr/local/bin/omenight-relay
UNIT=/etc/systemd/system/omenight-relay.service
STATE=/var/lib/omenight/state.json

ENDPOINT=""
KIND=wg
VERSION=""
FROM=""
UNINSTALL=0
PURGE=0

say() { printf '%s\n' "$*"; }
die() {
	printf 'omenight %s\n' "$*" >&2
	exit 1
}
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
	cat <<EOF
omenight relay installer

  --endpoint HOST   hostname or IP the nodes dial (needed on first install)
  --kind wg|awg     tunnel type for a first install (default wg)
  --version TAG     install a specific release (default latest)
  --from PATH       install a local binary instead of downloading
  --uninstall       stop the relay, delete its interfaces, rules and binary
  --purge           with --uninstall, also delete $STATE
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--endpoint) ENDPOINT="${2:?}"; shift 2 ;;
	--kind) KIND="${2:?}"; shift 2 ;;
	--version) VERSION="${2:?}"; shift 2 ;;
	--from) FROM="${2:?}"; shift 2 ;;
	--uninstall) UNINSTALL=1; shift ;;
	--purge) PURGE=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown option $1 (try --help)" ;;
	esac
done

[ "$(id -u)" = 0 ] || die "needs root"
[ -d /run/systemd/system ] || die "needs systemd"

if [ "$UNINSTALL" = 1 ]; then
	systemctl disable --now omenight-relay >/dev/null 2>&1 || true
	if [ -x "$BIN" ] && [ -f "$STATE" ]; then
		"$BIN" down -state "$STATE"
	fi
	rm -f "$UNIT" "$BIN"
	systemctl daemon-reload
	if [ "$PURGE" = 1 ]; then
		rm -rf /var/lib/omenight
		say "removed the relay and its state"
	else
		say "removed the relay, kept $STATE (node keys and slots), --purge deletes it"
	fi
	exit 0
fi

case "$KIND" in
wg) TOOL=wg ;;
awg) TOOL=awg ;;
*) die "--kind must be wg or awg" ;;
esac
[ -f "$STATE" ] || [ -n "$ENDPOINT" ] || die "--endpoint is required on the first install"

case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "no relay build for $(uname -m)" ;;
esac

# The kernel module ships with every distro kernel, only the tool is missing.
if [ "$KIND" = wg ] && ! have wg; then
	say "installing wireguard-tools"
	if have apt-get; then
		DEBIAN_FRONTEND=noninteractive apt-get install -y -q wireguard-tools >/dev/null
	elif have dnf; then
		dnf install -y -q wireguard-tools
	elif have pacman; then
		pacman -S --noconfirm --needed wireguard-tools >/dev/null
	elif have zypper; then
		zypper -n -q install wireguard-tools
	else
		die "install wireguard-tools by hand and run this again"
	fi
fi
have "$TOOL" || die "$TOOL is missing"
have ip || die "ip (iproute2) is missing"
if [ "$KIND" = awg ] && ! ip link add omenight-probe type amneziawg 2>/dev/null; then
	die "the amneziawg kernel module is missing, install it first or use --kind wg"
fi
ip link del omenight-probe 2>/dev/null || true

fetch() {
	if have curl; then
		curl -fsSL "$1" -o "$2"
	elif have wget; then
		wget -qO "$2" "$1"
	else
		die "need curl or wget"
	fi
}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
if [ -n "$FROM" ]; then
	[ -f "$FROM" ] || die "no such file $FROM"
	cp "$FROM" "$TMP/omenight-relay"
else
	if [ -n "$VERSION" ]; then
		URL="https://github.com/$REPO/releases/download/$VERSION"
	else
		URL="https://github.com/$REPO/releases/latest/download"
	fi
	ASSET="omenight-relay-linux-$ARCH"
	say "downloading $URL/$ASSET"
	fetch "$URL/$ASSET" "$TMP/$ASSET"
	fetch "$URL/SHA256SUMS" "$TMP/SHA256SUMS"
	(cd "$TMP" && grep " \./$ASSET\$" SHA256SUMS | sed "s| \./| |" | sha256sum -c - >/dev/null) ||
		die "checksum mismatch for $ASSET"
	mv "$TMP/$ASSET" "$TMP/omenight-relay"
fi
install -m 0755 "$TMP/omenight-relay" "$BIN"

if [ -f "$STATE" ]; then
	say "keeping the existing $STATE"
else
	"$BIN" init -state "$STATE" -endpoint "$ENDPOINT" -kind "$KIND"
fi

cat >"$UNIT" <<EOF
[Unit]
Description=Omenight relay
After=network-online.target
Wants=network-online.target

[Service]
User=root
CapabilityBoundingSet=CAP_NET_ADMIN
AmbientCapabilities=CAP_NET_ADMIN
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
StateDirectory=omenight
StateDirectoryMode=0700
RuntimeDirectory=omenight
ExecStart=$BIN serve -state $STATE -socket /run/omenight/control.sock
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable omenight-relay >/dev/null 2>&1
# Links survive the restart, so an upgrade does not drop node traffic.
systemctl restart omenight-relay
sleep 1
systemctl is-active --quiet omenight-relay || die "the service did not start, see journalctl -u omenight-relay"

say ""
say "relay is up. Every node gets its own UDP port, open them if this host has a firewall."
say "add a node with   omenight-relay add -name NAME"
say "watch nodes with  omenight-relay tui"
