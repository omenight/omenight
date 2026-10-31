#!/bin/bash
# End-to-end run of the relay and the pasted installer in throwaway namespaces.
# Run as a normal user. Needs wg, wg-quick, nft, python3 and user namespaces.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
if [ "${1:-}" != inner ]; then
	W=$(mktemp -d)
	(cd "$ROOT" && CGO_ENABLED=0 go build -o "$W/relay" ./cmd/omenight-relay)
	exec unshare --map-root-user --net --mount "$ROOT/e2e/netns.sh" inner "$W"
fi
W=$2
R="$W/relay"
ST="$W/st.json"
pass=0
fail=0
ok() { echo "ok   $*"; pass=$((pass + 1)); }
bad() { echo "FAIL $*"; fail=$((fail + 1)); }
check() {
	local name=$1
	shift
	if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name"; fi
}
cleanup() {
	kill $(jobs -p) ${NODE:-} ${INET:-} 2>/dev/null || true
	[ $fail = 0 ] && rm -rf "$W" || echo "kept $W"
}
trap cleanup EXIT

# The installer writes to /etc and checks /run/systemd, so give it copies.
cp -a /etc "$W/etc" 2>/dev/null || true
mkdir -p "$W/etc/wireguard"
mount --bind "$W/etc" /etc
mount -t tmpfs tmpfs /run

# The sleep must not hold the command substitution's pipe open.
ns() { unshare --net sleep infinity >/dev/null 2>&1 & echo $!; }
NODE=$(ns)
INET=$(ns)
sleep 0.3
in_node() { nsenter -t "$NODE" -n "$@"; }
in_inet() { nsenter -t "$INET" -n "$@"; }

# relay 192.0.2.1 --- 192.0.2.2 node 203.0.113.2 --- 203.0.113.1 internet
ip link set lo up
ip link add r0 type veth peer name n0 netns "$NODE"
ip addr add 192.0.2.1/24 dev r0
ip link set r0 up
# Loose rp_filter drops replies whose source the main table cannot route, and
# a real VPS always has a default. This one leads nowhere, so nothing leaks.
ip link add d0 type dummy
ip addr add 100.127.0.1/24 dev d0
ip link set d0 up
ip route add default via 100.127.0.2 dev d0
in_node ip link set lo up
in_node ip addr add 192.0.2.2/24 dev n0
in_node ip link set n0 up
in_node ip link add n1 type veth peer name i0 netns "$INET"
in_node ip addr add 203.0.113.2/24 dev n1
in_node ip link set n1 up
in_node ip route add default via 203.0.113.1
in_inet ip link set lo up
in_inet ip addr add 203.0.113.1/24 dev i0
in_inet ip addr add 198.51.100.1/32 dev lo
in_inet ip addr add 192.168.1.1/32 dev lo
in_inet ip link set i0 up

# Answers every connection with the address it came from.
echo_server='import socket,sys
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("0.0.0.0", int(sys.argv[1]))); s.listen()
while True:
    c,a=s.accept(); c.send(a[0].encode()); c.close()'
in_inet python3 -c "$echo_server" 8080 &
in_node python3 -c "$echo_server" 9000 &

# get HOST PORT [MARK] prints what the far end saw, or the error.
get() {
	python3 - "$@" <<'PY'
import socket,sys
host, port = sys.argv[1], int(sys.argv[2])
s = socket.socket()
if len(sys.argv) > 3:
    s.setsockopt(socket.SOL_SOCKET, socket.SO_MARK, int(sys.argv[3]))
s.settimeout(2)
try:
    s.connect((host, port)); print(s.recv(64).decode())
except OSError as e:
    print("error", e.__class__.__name__, e.strerror or "")
PY
}
sleep 0.3

"$R" init -state "$ST" -endpoint 192.0.2.1 >/dev/null
"$R" add -state "$ST" -name n1 >"$W/paste.sh" 2>/dev/null
"$R" serve -state "$ST" -socket "$W/c.sock" >"$W/relay.log" 2>&1 &
sleep 1

check "relay made om1" ip link show om1
check "relay rule fwmark 7701" sh -c 'ip rule show pref 7700 | grep -q "fwmark 0x1e15 lookup 7701"'
check "relay v4 route in table 7701" sh -c 'ip route show table 7701 | grep -q "default dev om1"'
check "relay v6 blackhole in table 7701" sh -c 'ip -6 route show table 7701 | grep -q unreachable'
check "relay rp_filter loose on om1" sh -c '[ "$(cat /proc/sys/net/ipv4/conf/om1/rp_filter)" = 2 ]'

out=$(in_node sh "$W/paste.sh" 2>&1) || true
echo "$out" | sed 's/^/     node | /'
grep -q 'handshake with' <<<"$out" && ok "node handshake" || bad "node handshake"
check "node has no default route through the tunnel" sh -c "! nsenter -t $NODE -n ip route show default | grep -q omenight"

got=$(get 198.51.100.1 8080 7701)
[ "$got" = 203.0.113.2 ] && ok "marked traffic exits via the node ($got)" || bad "marked traffic exits via the node ($got)"
got=$(get 198.51.100.1 8080)
case "$got" in error*) ok "unmarked traffic does not leak ($got)" ;; *) bad "unmarked traffic leaked ($got)" ;; esac
got=$(get 192.168.1.1 8080 7701)
case "$got" in error*) ok "node LAN ranges are blocked ($got)" ;; *) bad "node LAN reachable ($got)" ;; esac
got=$(get 10.77.0.6 9000)
case "$got" in error*Timeout*) ok "node input closed ($got)" ;; *) bad "node input open ($got)" ;; esac
got=$(get 10.77.0.6 22)
case "$got" in *Refused*) ok "node ssh port open to the relay ($got)" ;; *) bad "node ssh port ($got)" ;; esac

snap=$(python3 -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); print(s.makefile().read())' "$W/c.sock")
grep -q '"online":true' <<<"$snap" && ok "snapshot shows n1 online" || bad "snapshot shows n1 online"

"$R" revoke -state "$ST" -name n1 >/dev/null
sleep 3
got=$(get 198.51.100.1 8080 7701)
case "$got" in error*) ok "revoked node carries nothing ($got)" ;; *) bad "revoked node still works ($got)" ;; esac
# The relay learns the node's endpoint again only from its next keepalive.
"$R" approve -state "$ST" -name n1 >/dev/null
for _ in $(seq 15); do
	got=$(get 198.51.100.1 8080 7701)
	[ "$got" = 203.0.113.2 ] && break
	sleep 2
done
[ "$got" = 203.0.113.2 ] && ok "approved again ($got)" || bad "approve did not restore ($got)"

"$R" remove -state "$ST" -name n1 >"$W/uninstall.sh" 2>/dev/null
sleep 3
check "remove deleted om1" sh -c '! ip link show om1'
check "remove deleted the rule" sh -c '! ip rule show pref 7700 | grep -q .'
in_node sh "$W/uninstall.sh" >/dev/null 2>&1 || true
check "node uninstall removed the interface" sh -c "! nsenter -t $NODE -n ip link show omenight"
check "node uninstall removed the nft table" sh -c "! nsenter -t $NODE -n nft list table inet omenight"

echo "$pass passed, $fail failed"
[ $fail = 0 ] || { echo "--- relay log"; cat "$W/relay.log"; }
exit $fail
