#!/usr/bin/env bash
# Build the ext4 root filesystem used by Cloud Hypervisor sandboxes.
#
#   scripts/ch-mkrootfs.sh <output.ext4> [size]
#
# Works with Docker (Linux) or Apple's container runtime (macOS), selected
# by SBX_RUNTIME. Everything that needs root runs inside a container, so file
# ownership in the image is correct even when built from a Mac.
set -euo pipefail

out=${1:?usage: ch-mkrootfs.sh <output.ext4> [size]}
size=${2:-8G}
rt=${SBX_RUNTIME:-docker}
root="$(cd "$(dirname "$0")/.." && pwd)"
outdir="$(cd "$(dirname "$out")" && pwd)"
name="$(basename "$out")"

quiet=() bquiet=()
if [ "$rt" = container ]; then quiet=(--progress none); bquiet=(--progress plain); fi
# Docker only looks for a file named Dockerfile unless told otherwise.
build() { "$rt" build ${bquiet[@]+"${bquiet[@]}"} -f "$1/Containerfile" -t "$2" "$1" >/dev/null; }

echo "==> building sbx-base and sbx-ch images ($rt)"
build "$root/image" sbx-base:latest
build "$root/image/ch" sbx-ch:latest

echo "==> writing $out ($size, sparse)"
# Stream the image's filesystem out as tar, and unpack it as root inside a
# second container that has mkfs.ext4.
"$rt" run --rm ${quiet[@]+"${quiet[@]}"} --entrypoint tar sbx-ch:latest \
    -C / --numeric-owner \
    --exclude=./proc --exclude=./sys --exclude=./dev --exclude=./run --exclude=./tmp \
    -cf - . |
  "$rt" run --rm -i ${quiet[@]+"${quiet[@]}"} \
    --mount "type=bind,source=$outdir,target=/out" \
    --entrypoint bash debian:bookworm-slim -c "
      set -euo pipefail
      apt-get update -qq >/dev/null && apt-get install -y -qq e2fsprogs >/dev/null
      mkdir /r && tar -xpf - -C /r --numeric-owner
      mkdir -p /r/proc /r/sys /r/dev /r/run /r/tmp
      chmod 1777 /r/tmp
      mkfs.ext4 -q -F -L sbx-root -d /r /out/$name.tmp $size
      mv /out/$name.tmp /out/$name
    "
echo "==> done: $out"
