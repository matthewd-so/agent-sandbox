# Latest Node.js LTS, unpacked straight into the flavor.
arch=$(uname -m); case "$arch" in aarch64) arch=arm64 ;; x86_64) arch=x64 ;; esac
version=$(curl -fsSL https://nodejs.org/dist/index.json | jq -r '[.[] | select(.lts)][0].version')
echo "node $version ($arch)"
curl -fsSL "https://nodejs.org/dist/$version/node-$version-linux-$arch.tar.xz" |
  tar -xJ --strip-components=1 -C "$OUT"
rm -rf "$OUT"/{CHANGELOG.md,README.md,share/doc}
