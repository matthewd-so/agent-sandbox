export PATH="$FLAVOR_DIR/bin:$PATH"
mkdir -p "$HOME/.docker"
[ -e "$HOME/.docker/cli-plugins" ] || ln -s "$FLAVOR_DIR/cli-plugins" "$HOME/.docker/cli-plugins"
