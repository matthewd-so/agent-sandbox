arch=$(uname -m)
base="https://download.docker.com/linux/static/stable/$arch"
version=$(curl -fsSL "$base/" | grep -o 'docker-[0-9][0-9.]*\.tgz' | sed 's/^docker-//; s/\.tgz$//' | sort -uV | tail -n1)
echo "docker $version ($arch)"
mkdir -p "$OUT/bin" "$OUT/cli-plugins"
curl -fsSL "$base/docker-$version.tgz" |
  tar -xz --strip-components=1 -C "$OUT/bin"
case "$arch" in aarch64) carch=aarch64 ;; *) carch=x86_64 ;; esac
compose=$(curl -fsSL https://api.github.com/repos/docker/compose/releases/latest | jq -r .tag_name)
curl -fsSL -o "$OUT/cli-plugins/docker-compose" \
  "https://github.com/docker/compose/releases/download/$compose/docker-compose-linux-$carch"
chmod +x "$OUT/cli-plugins/docker-compose"
