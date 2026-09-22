mkdir -p "$OUT/bin"
curl -LsSf https://astral.sh/uv/install.sh |
  env UV_INSTALL_DIR="$OUT/bin" UV_NO_MODIFY_PATH=1 sh
UV_PYTHON_INSTALL_DIR="$OUT/python" "$OUT/bin/uv" python install 3.13
# $OUT is the same path the flavor is mounted at in sandboxes (/flv/python),
# so an absolute link is fine.
ln -sf "$(UV_PYTHON_INSTALL_DIR="$OUT/python" "$OUT/bin/uv" python find --managed-python 3.13)" "$OUT/bin/python3.13"
"$OUT/bin/python3.13" --version
