#!/usr/bin/env bash
# Prepare a Linux server (Debian 12+ or Ubuntu 24.04+, x86_64 or arm64, with
# KVM) to run sbx sandboxes on Cloud Hypervisor. Safe to re-run; run it at
# boot as well, since the bridge and firewall rules are not persistent.
#
#   sudo scripts/ch-host-setup.sh
set -euo pipefail

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
[ -e /dev/kvm ] || { echo "/dev/kvm missing: this host cannot run KVM guests" >&2; exit 1; }

repo="$(cd "$(dirname "$0")/.." && pwd)"
ROOT=${SBX_HOST_ROOT:-/var/lib/sbx}
BR=${SBX_HOST_BRIDGE:-sbxbr0}
SUBNET=${SBX_HOST_SUBNET:-10.77.0.0/24}
GW="${SUBNET%.*}.1"            # first host address of the subnet
BITS="${SUBNET#*/}"
arch=$(uname -m)

echo "==> packages"
apt-get update -qq
apt-get install -y -qq virtiofsd iproute2 iptables openssh-client e2fsprogs \
  docker.io jq curl git perl golang-go >/dev/null

gh_latest() { curl -fsSL "https://api.github.com/repos/$1/releases/latest" | jq -r .tag_name; }

echo "==> cloud-hypervisor"
tag=$(gh_latest cloud-hypervisor/cloud-hypervisor)
asset=cloud-hypervisor-static; [ "$arch" = aarch64 ] && asset=cloud-hypervisor-static-aarch64
curl -fsSL -o /usr/local/bin/cloud-hypervisor \
  "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/$tag/$asset"
chmod 0755 /usr/local/bin/cloud-hypervisor
echo "    $tag"

echo "==> guest kernel"
mkdir -p "$ROOT/images"
ktag=$(gh_latest cloud-hypervisor/linux)
kasset=vmlinux-x86_64; [ "$arch" = aarch64 ] && kasset=Image-arm64
curl -fsSL -o "$ROOT/images/vmlinux" \
  "https://github.com/cloud-hypervisor/linux/releases/download/$ktag/$kasset"
echo "    $ktag"

echo "==> bridge $BR ($GW/$BITS) and NAT"
ip link show "$BR" >/dev/null 2>&1 || ip link add "$BR" type bridge
ip addr replace "$GW/$BITS" dev "$BR"
ip link set "$BR" up
sysctl -qw net.ipv4.ip_forward=1
rule() { iptables "$@" 2>/dev/null; }
add() { local t=$1; shift; rule -t "$t" -C "$@" || iptables -t "$t" -I "$@"; }
add nat POSTROUTING -s "$SUBNET" ! -o "$BR" -j MASQUERADE
# Docker sets the FORWARD policy to DROP; let sandbox traffic through.
add filter FORWARD -i "$BR" ! -o "$BR" -j ACCEPT
add filter FORWARD -o "$BR" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
# Sandboxes may not open connections to the host itself (replies to the
# host's own SSH sessions are still allowed).
rule -C INPUT -i "$BR" -j DROP || iptables -A INPUT -i "$BR" -j DROP
rule -C INPUT -i "$BR" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT ||
  iptables -I INPUT -i "$BR" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

echo "==> sbx-host"
(cd "$repo" && CGO_ENABLED=0 go build -o bin/sbx-host ./cmd/sbx-host)
ln -sf "$repo/bin/sbx-host" /usr/local/bin/sbx-host
ln -sf "$repo/sbx" /usr/local/bin/sbx

echo "==> base image and flavors (Docker builds them on this host)"
export SBX_RUNTIME=docker SBX_HOME="$ROOT" SBX_PROXY_AUTO=0
"$repo/sbx" image
"$repo/sbx" flavor build default
"$repo/scripts/ch-mkrootfs.sh" "$ROOT/images/rootfs.ext4"

sbx-host init
echo
echo "Done. From your Mac: export SBX_REMOTE=$(logname 2>/dev/null || echo you)@$(hostname -f)"
