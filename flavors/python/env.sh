export PATH="$FLAVOR_DIR/bin:$PATH"
# Let uv find the preinstalled Python, but install anything new under $HOME.
export UV_PYTHON_INSTALL_DIR="$HOME/.local/share/uv/python"
export UV_PYTHON="$FLAVOR_DIR/bin/python3.13"
