arch=$(uname -m); case "$arch" in aarch64) arch=arm64 ;; x86_64) arch=amd64 ;; esac
version=$(curl -fsSL https://api.github.com/repos/cli/cli/releases/latest | jq -r .tag_name)
echo "gh $version ($arch)"
curl -fsSL "https://github.com/cli/cli/releases/download/$version/gh_${version#v}_linux_$arch.tar.gz" |
  tar -xz --strip-components=1 -C "$OUT"
