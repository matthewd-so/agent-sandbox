arch=$(uname -m); case "$arch" in aarch64) arch=arm64 ;; x86_64) arch=amd64 ;; esac
version=$(curl -fsSL 'https://go.dev/dl/?mode=json' | jq -r '.[0].version')
echo "$version ($arch)"
curl -fsSL "https://go.dev/dl/$version.linux-$arch.tar.gz" | tar -xz --strip-components=1 -C "$OUT"
