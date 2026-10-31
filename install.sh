#!/bin/sh
# omenight node installer, normally pasted inside the block from `omenight-relay add`.
# Standalone use is `sh install.sh --conf node.conf` or `sh install.sh --uninstall`.

IFACE=omenight
CONF_DIR=/etc/omenight

say() { printf '%s\n' "$*"; }
die() {
	printf 'omenight %s\n' "$*" >&2
	exit 1
}
have() { command -v "$1" >/dev/null 2>&1; }

conf_get() {
	printf '%s\n' "$CONF" | sed -n "s/^$1[[:space:]]*=[[:space:]]*//p" | head -n 1
}

platform() {
	if [ -f /etc/openwrt_release ] && have uci; then
		echo openwrt
	else
		echo linux
	fi
}

load_conf() {
	CONF=$(printf '%s\n' "$CONF" | tr -d '\r')
	NAME=$(printf '%s\n' "$CONF" | sed -n 's/^# omenight node //p' | head -n 1)
	PRIV=$(conf_get PrivateKey)
	ADDR=$(conf_get Address)
	PUB=$(conf_get PublicKey)
	EP=$(conf_get Endpoint)
	ALLOWED=$(conf_get AllowedIPs)
	KA=$(conf_get PersistentKeepalive)
	[ -n "$PRIV" ] && [ -n "$ADDR" ] && [ -n "$PUB" ] && [ -n "$EP" ] && [ -n "$ALLOWED" ] ||
		die "the config is incomplete, print it again with omenight-relay show"
	case "$PRIV$ADDR$PUB$EP$ALLOWED$KA" in
	*[!]A-Za-z0-9+/=.:[-]*) die "unexpected characters in the config" ;;
	esac
	RELAY_IP=${ALLOWED%/*}
	EP_HOST=${EP%:*}
	EP_PORT=${EP##*:}
	EP_HOST=${EP_HOST#\[}
	EP_HOST=${EP_HOST%\]}
	KIND=wg
	[ -n "$(conf_get Jc)" ] && KIND=awg
	if [ "$KIND" = awg ]; then
		TOOL=awg
	else
		TOOL=wg
	fi
}

# Packages differ between releases (apk since 25.x, opkg before), so this
# only tries and leaves the error to the caller.
openwrt_packages() {
	if have apk; then
		apk update >/dev/null 2>&1
		apk add "$@"
	elif have opkg; then
		opkg update >/dev/null 2>&1
		opkg install "$@"
	else
		return 1
	fi
}

openwrt_install() {
	if [ "$KIND" = awg ]; then
		proto=amneziawg
		pkgs="kmod-amneziawg amneziawg-tools"
	else
		proto=wireguard
		pkgs="kmod-wireguard wireguard-tools"
	fi
	if [ ! -f "/lib/netifd/proto/$proto.sh" ] || ! have "$TOOL"; then
		say "installing $pkgs"
		openwrt_packages $pkgs || die "could not install $pkgs, add them by hand and paste again"
		[ -f "/lib/netifd/proto/$proto.sh" ] || die "$proto netifd support is still missing"
	fi
	# netifd loads protocol handlers only at startup, so a freshly installed
	# one needs a restart. Asking netifd also covers an earlier paste.
	restart=0
	ubus call network get_proto_handlers | grep -q "\"$proto\": {" || restart=1
	z=0
	while zn=$(uci -q get "firewall.@zone[$z].name"); do
		[ "$zn" = wan ] && break
		z=$((z + 1))
	done
	[ "${zn:-}" = wan ] || die "no firewall zone named wan"
	[ "$(uci -q get "firewall.@zone[$z].masq")" = 1 ] ||
		say "warning, the wan zone has no masquerade, tunnel traffic will not get out"

	ifdown "$IFACE" 2>/dev/null || true
	for s in network.$IFACE network.${IFACE}_relay firewall.$IFACE firewall.${IFACE}_wan \
		firewall.${IFACE}_ssh firewall.${IFACE}_ping; do
		uci -q delete "$s" || true
	done

	{
		echo "set network.$IFACE=interface"
		echo "set network.$IFACE.proto='$proto'"
		echo "set network.$IFACE.private_key='$PRIV'"
		echo "add_list network.$IFACE.addresses='$ADDR'"
		if [ "$KIND" = awg ]; then
			for k in Jc Jmin Jmax S1 S2 H1 H2 H3 H4; do
				echo "set network.$IFACE.awg_$(echo "$k" | tr 'A-Z' 'a-z')='$(conf_get "$k")'"
			done
		fi
		echo "set network.${IFACE}_relay=${proto}_$IFACE"
		echo "set network.${IFACE}_relay.description='omenight relay'"
		echo "set network.${IFACE}_relay.public_key='$PUB'"
		echo "set network.${IFACE}_relay.endpoint_host='$EP_HOST'"
		echo "set network.${IFACE}_relay.endpoint_port='$EP_PORT'"
		echo "set network.${IFACE}_relay.persistent_keepalive='${KA:-25}'"
		echo "add_list network.${IFACE}_relay.allowed_ips='$ALLOWED'"
		echo "set network.${IFACE}_relay.route_allowed_ips='0'"

		# Forwarding only to wan, so the relay cannot reach the LAN. Input
		# stays closed apart from ssh and ping from the relay.
		echo "set firewall.$IFACE=zone"
		echo "set firewall.$IFACE.name='$IFACE'"
		echo "add_list firewall.$IFACE.network='$IFACE'"
		echo "set firewall.$IFACE.input='REJECT'"
		echo "set firewall.$IFACE.output='ACCEPT'"
		echo "set firewall.$IFACE.forward='REJECT'"
		echo "set firewall.${IFACE}_wan=forwarding"
		echo "set firewall.${IFACE}_wan.src='$IFACE'"
		echo "set firewall.${IFACE}_wan.dest='wan'"
		echo "set firewall.${IFACE}_ssh=rule"
		echo "set firewall.${IFACE}_ssh.name='omenight-ssh'"
		echo "set firewall.${IFACE}_ssh.src='$IFACE'"
		echo "set firewall.${IFACE}_ssh.src_ip='$RELAY_IP'"
		echo "set firewall.${IFACE}_ssh.proto='tcp'"
		echo "set firewall.${IFACE}_ssh.dest_port='22'"
		echo "set firewall.${IFACE}_ssh.target='ACCEPT'"
		echo "set firewall.${IFACE}_ping=rule"
		echo "set firewall.${IFACE}_ping.name='omenight-ping'"
		echo "set firewall.${IFACE}_ping.src='$IFACE'"
		echo "set firewall.${IFACE}_ping.proto='icmp'"
		echo "set firewall.${IFACE}_ping.icmp_type='echo-request'"
		echo "set firewall.${IFACE}_ping.target='ACCEPT'"
	} | uci batch

	uci commit network
	uci commit firewall
	/etc/init.d/firewall reload >/dev/null 2>&1
	if [ "$restart" = 1 ]; then
		say "restarting the network to load $proto support, connections drop for a few seconds"
		/etc/init.d/network restart
	else
		ubus call network reload
		ifup "$IFACE"
	fi
}

openwrt_uninstall() {
	ifdown "$IFACE" 2>/dev/null || true
	for s in network.$IFACE network.${IFACE}_relay firewall.$IFACE firewall.${IFACE}_wan \
		firewall.${IFACE}_ssh firewall.${IFACE}_ping; do
		uci -q delete "$s" || true
	done
	uci commit network
	uci commit firewall
	/etc/init.d/firewall reload >/dev/null 2>&1
}

linux_paths() {
	if [ "$KIND" = awg ]; then
		QUICK=awg-quick
		QUICK_DIR=/etc/amnezia/amneziawg
	else
		QUICK=wg-quick
		QUICK_DIR=/etc/wireguard
	fi
}

linux_install() {
	linux_paths
	have "$QUICK" || die "$QUICK is missing. Install $([ "$KIND" = awg ] && echo 'AmneziaWG (kernel module or amneziawg-go, plus amneziawg-tools)' || echo wireguard-tools) and paste again"
	have nft || die "nft is missing, install nftables and paste again"
	if have iptables && iptables -S FORWARD 2>/dev/null | grep -q '^-P FORWARD DROP'; then
		say "warning, the iptables FORWARD policy is DROP (docker, ufw), tunnel traffic may not get out"
	fi

	mkdir -p "$CONF_DIR" "$QUICK_DIR"
	# Redefining the table on every up keeps re-runs idempotent.
	cat >"$CONF_DIR/nft.conf" <<EOF
table inet omenight
delete table inet omenight
table inet omenight {
	chain input {
		type filter hook input priority -5; policy accept;
		iifname "$IFACE" ct state established,related accept
		iifname "$IFACE" ip saddr $RELAY_IP tcp dport 22 accept
		iifname "$IFACE" icmp type echo-request accept
		iifname "$IFACE" drop
	}
	chain forward {
		type filter hook forward priority -5; policy accept;
		iifname "$IFACE" ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/3 } drop
	}
	chain postrouting {
		type nat hook postrouting priority 100; policy accept;
		iifname "$IFACE" oifname != "$IFACE" masquerade
	}
}
EOF
	printf '%s\n' "$CONF" | awk -v up="sysctl -qw net.ipv4.ip_forward=1; nft -f $CONF_DIR/nft.conf" \
		-v down="nft delete table inet omenight" \
		'{ print } /^\[Interface\]/ { print "PostUp = " up; print "PostDown = " down }' \
		>"$QUICK_DIR/$IFACE.conf"
	chmod 600 "$QUICK_DIR/$IFACE.conf"

	if [ -d /run/systemd/system ]; then
		systemctl enable "$QUICK@$IFACE" >/dev/null 2>&1
		systemctl restart "$QUICK@$IFACE" || die "$QUICK@$IFACE failed, see journalctl -u $QUICK@$IFACE"
	else
		"$QUICK" down "$IFACE" >/dev/null 2>&1 || true
		"$QUICK" up "$IFACE" || die "$QUICK up $IFACE failed"
		say "warning, no systemd, so this does not come back after a reboot. Run '$QUICK up $IFACE' at boot"
	fi
	say "note, net.ipv4.ip_forward is now 1 on this host"
}

linux_uninstall() {
	for q in awg-quick wg-quick; do
		if [ -d /run/systemd/system ]; then
			systemctl disable --now "$q@$IFACE" >/dev/null 2>&1 || true
		fi
		have "$q" && "$q" down "$IFACE" >/dev/null 2>&1 || true
	done
	rm -f "/etc/amnezia/amneziawg/$IFACE.conf" "/etc/wireguard/$IFACE.conf"
	have nft && nft delete table inet omenight 2>/dev/null || true
}

# The relay shows the node online after the first handshake, so wait for it
# here and say whether the tunnel actually works.
wait_handshake() {
	i=0
	while [ $i -lt 15 ]; do
		hs=$("$TOOL" show "$IFACE" latest-handshakes 2>/dev/null | awk '{ print $2 }')
		if [ -n "$hs" ] && [ "$hs" != 0 ]; then
			say "handshake with $EP done, the relay sees ${NAME:-this node} online"
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	say "no handshake with $EP after 15s. Check that UDP $EP_PORT reaches the relay and that the node is still active there"
	return 1
}

main() {
	ACTION=${OMENIGHT_ACTION:-install}
	CONF=${OMENIGHT_CONF:-}
	while [ $# -gt 0 ]; do
		case "$1" in
		--conf)
			[ -f "${2:-}" ] || die "--conf needs a file"
			CONF=$(cat "$2")
			shift 2
			;;
		--uninstall)
			ACTION=uninstall
			shift
			;;
		*) die "unknown option $1" ;;
		esac
	done
	[ "$(id -u)" = 0 ] || die "needs root"
	set -eu

	P=$(platform)
	if [ "$ACTION" = uninstall ]; then
		"${P}_uninstall"
		rm -rf "$CONF_DIR"
		say "omenight removed from this node"
		return 0
	fi

	[ -n "$CONF" ] || die "no config, pass --conf or paste the block from omenight-relay add"
	load_conf
	say "setting up ${NAME:-omenight node} ($KIND, $P), tunnel $ADDR to $EP"
	"${P}_install"
	wait_handshake || true
}

main "$@" </dev/null
