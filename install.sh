#!/bin/sh
# omenight agent installer.
#
#   curl -fsSL https://raw.githubusercontent.com/omenight/omenight/main/install.sh | sh -s -- \
#     --relay relay.example.com:2223 --name node-a --forward 127.0.0.1:1085=127.0.0.1:1080
#
# Runs on any distro with a POSIX shell. Detects architecture and init
# system, installs one static binary, writes a config, and prints the public
# key to approve on the relay. Safe to re-run: the identity is preserved.

set -eu

REPO="${OMENIGHT_REPO:-omenight/omenight}"
PREFIX="${OMENIGHT_PREFIX:-/usr/local}"
CONF_DIR="${OMENIGHT_CONF:-/etc/omenight}"
SVC="omenight-agent"
BIN="$PREFIX/bin/$SVC"

RELAY=""
NAME=""
SOCKS=""
VERSION=""
FROM=""
FORWARDS=""
EGRESS=""
UNINSTALL=0
PURGE=0
START=1

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
	cat <<EOF
omenight agent installer

  --relay HOST:PORT       relay address (required)
  --name NAME             client name, the SSH username (default: hostname)
  --forward L=T           forward relay L to local T (repeatable)
  --egress PORT           shorthand: SOCKS on 127.0.0.1:1080, forward PORT to it
  --socks ADDR            run the built-in SOCKS5 server, e.g. 127.0.0.1:1080
  --version TAG           install a specific release (default: latest)
  --from PATH             install a local binary instead of downloading
  --no-start              install but do not start the service
  --uninstall             stop and remove the service and binary
  --purge                 with --uninstall, also remove $CONF_DIR

Examples:
  --forward 127.0.0.1:1085=127.0.0.1:1080 --socks 127.0.0.1:1080
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--relay) RELAY="${2:?}"; shift 2 ;;
	--name) NAME="${2:?}"; shift 2 ;;
	--forward) FORWARDS="$FORWARDS $2"; shift 2 ;;
	--egress) EGRESS="${2:?}"; shift 2 ;;
	--socks) SOCKS="${2:?}"; shift 2 ;;
	--version) VERSION="${2:?}"; shift 2 ;;
	--from) FROM="${2:?}"; shift 2 ;;
	--no-start) START=0; shift ;;
	--uninstall) UNINSTALL=1; shift ;;
	--purge) PURGE=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown option: $1 (try --help)" ;;
	esac
done

[ "$(id -u)" = 0 ] || die "run me as root (sudo $0 ...)"

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	armv7l | armv7) echo armv7 ;;
	armv6l | armv6) echo armv6 ;;
	*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

detect_init() {
	if [ -d /run/systemd/system ] && have systemctl; then
		echo systemd
	elif have rc-service && [ -d /etc/init.d ]; then
		echo openrc
	elif have sv && [ -d /etc/sv ]; then
		echo runit
	else
		echo none
	fi
}

fetch_to() {
	url="$1"
	out="$2"
	if have curl; then
		curl -fsSL "$url" -o "$out"
	elif have wget; then
		wget -qO "$out" "$url"
	else
		die "need curl or wget"
	fi
}

ensure_user() {
	id "$SVC" >/dev/null 2>&1 && return 0
	if have useradd; then
		useradd --system --no-create-home --home-dir "$CONF_DIR" \
			--shell /sbin/nologin "$SVC" 2>/dev/null ||
			useradd -r -s /sbin/nologin "$SVC"
	elif have adduser; then
		adduser -S -H -h "$CONF_DIR" -s /sbin/nologin "$SVC" 2>/dev/null ||
			adduser --system --no-create-home --shell /sbin/nologin "$SVC"
	else
		die "no useradd or adduser"
	fi
}

stop_service() {
	case "$(detect_init)" in
	systemd) systemctl disable --now "$SVC" 2>/dev/null || true ;;
	openrc) rc-service "$SVC" stop 2>/dev/null || true; rc-update del "$SVC" default 2>/dev/null || true ;;
	runit)
		rm -f /var/service/"$SVC" /etc/service/"$SVC"
		sv stop "$SVC" 2>/dev/null || true
		;;
	esac
}

do_uninstall() {
	stop_service
	rm -f "/etc/systemd/system/$SVC.service" "/etc/init.d/$SVC"
	rm -rf "/etc/sv/$SVC"
	have systemctl && systemctl daemon-reload 2>/dev/null || true
	rm -f "$BIN"
	say "removed $SVC"
	if [ "$PURGE" = 1 ]; then
		rm -rf "$CONF_DIR"
		say "removed $CONF_DIR"
	else
		say "kept $CONF_DIR (identity, config) — use --purge to remove"
	fi
	exit 0
}

[ "$UNINSTALL" = 1 ] && do_uninstall

# --egress is the common case: an exit node serving SOCKS.
if [ -n "$EGRESS" ]; then
	[ -n "$SOCKS" ] || SOCKS="127.0.0.1:1080"
	FORWARDS="$FORWARDS 127.0.0.1:$EGRESS=$SOCKS"
fi

# Interactive fallback when values are missing.
if [ -z "$RELAY" ] && [ -t 0 ]; then
	printf 'relay address (host:port): '
	read -r RELAY
fi
[ -n "$RELAY" ] || die "--relay is required"

# Default the label to the machine's own name, so a fresh box needs one flag.
if [ -z "$NAME" ]; then
	NAME="$(hostname -s 2>/dev/null || hostname)"
fi
NAME="$(printf '%s' "$NAME" | tr -c 'A-Za-z0-9._-' '-')"
[ -n "$NAME" ] || die "could not determine a name, pass --name"

case "$RELAY$NAME$SOCKS$FORWARDS" in
*'"'* | *\\*) die "quotes and backslashes are not allowed in values" ;;
esac

INIT="$(detect_init)"
ARCH="$(detect_arch)"
say "installing $SVC for linux/$ARCH, init: $INIT"

# --- binary -----------------------------------------------------------------

install -d "$PREFIX/bin"
if [ -n "$FROM" ]; then
	[ -f "$FROM" ] || die "no such file: $FROM"
	install -m 0755 "$FROM" "$BIN"
	say "installed local binary $FROM"
else
	if [ -n "$VERSION" ]; then
		URL="https://github.com/$REPO/releases/download/$VERSION/$SVC-linux-$ARCH"
	else
		URL="https://github.com/$REPO/releases/latest/download/$SVC-linux-$ARCH"
	fi
	TMP="$(mktemp)"
	trap 'rm -f "$TMP"' EXIT
	say "downloading $URL"
	fetch_to "$URL" "$TMP"
	install -m 0755 "$TMP" "$BIN"
fi

# --- config -----------------------------------------------------------------

ensure_user
install -d -m 0750 "$CONF_DIR"
chown "$SVC" "$CONF_DIR"

FORWARD_JSON=""
for f in $FORWARDS; do
	L="${f%%=*}"
	T="${f#*=}"
	[ "$L" != "$f" ] || die "--forward $f: want listen=target"
	SEP=","
	[ -z "$FORWARD_JSON" ] && SEP=""
	FORWARD_JSON="$FORWARD_JSON$SEP
    {\"listen\": \"$L\", \"target\": \"$T\"}"
done

{
	printf '{\n'
	printf '  "relay": "%s",\n' "$RELAY"
	printf '  "name": "%s",\n' "$NAME"
	printf '  "key": "%s/identity",\n' "$CONF_DIR"
	printf '  "known_hosts": "%s/known_hosts",\n' "$CONF_DIR"
	[ -n "$SOCKS" ] && printf '  "socks": "%s",\n' "$SOCKS"
	if [ -n "$FORWARD_JSON" ]; then
		printf '  "forwards": [%s\n  ]\n' "$FORWARD_JSON"
	else
		printf '  "forwards": []\n'
	fi
	printf '}\n'
} >"$CONF_DIR/agent.json.tmp"

mv "$CONF_DIR/agent.json.tmp" "$CONF_DIR/agent.json"
chmod 0640 "$CONF_DIR/agent.json"

# Create the identity as root, then hand the whole directory to the service
# user. Re-runs keep the existing identity.
"$BIN" pubkey -key "$CONF_DIR/identity" >/dev/null
chown -R "$SVC" "$CONF_DIR"
say "wrote $CONF_DIR/agent.json"

# --- service ----------------------------------------------------------------

case "$INIT" in
systemd)
	cat >"/etc/systemd/system/$SVC.service" <<EOF
[Unit]
Description=Omenight reverse tunnel agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SVC
ExecStart=$BIN run -config $CONF_DIR/agent.json
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$CONF_DIR

[Install]
WantedBy=multi-user.target
EOF
	systemctl daemon-reload
	systemctl enable "$SVC" >/dev/null 2>&1 || true
	[ "$START" = 1 ] && systemctl restart "$SVC"
	;;
openrc)
	cat >"/etc/init.d/$SVC" <<EOF
#!/sbin/openrc-run
description="Omenight reverse tunnel agent"
command="$BIN"
command_args="run -config $CONF_DIR/agent.json"
command_user="$SVC"
command_background=true
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/var/log/$SVC.log"
error_log="/var/log/$SVC.log"
depend() { need net; after firewall; }
start_pre() { checkpath --directory --mode 0750 --owner $SVC /var/log; }
EOF
	chmod 0755 "/etc/init.d/$SVC"
	rc-update add "$SVC" default >/dev/null 2>&1 || true
	[ "$START" = 1 ] && rc-service "$SVC" restart
	;;
runit)
	mkdir -p "/etc/sv/$SVC"
	cat >"/etc/sv/$SVC/run" <<EOF
#!/bin/sh
exec 2>&1
exec chpst -u $SVC $BIN run -config $CONF_DIR/agent.json
EOF
	chmod 0755 "/etc/sv/$SVC/run"
	SV_DIR=/var/service
	[ -d /etc/service ] && SV_DIR=/etc/service
	ln -sfn "/etc/sv/$SVC" "$SV_DIR/$SVC"
	[ "$START" = 1 ] && sv restart "$SVC" 2>/dev/null || true
	;;
*)
	say ""
	say "no supported init system found. run it yourself:"
	say "  $BIN run -config $CONF_DIR/agent.json"
	;;
esac

# --- finish -----------------------------------------------------------------

KEY="$("$BIN" pubkey -key "$CONF_DIR/identity")"
say ""
say "installed. approve this key on the relay:"
say ""
say "  omenight-relay approve -name $NAME"
say "  omenight-relay allow   -name $NAME -addr <listen addr>"
say ""
say "$KEY"
