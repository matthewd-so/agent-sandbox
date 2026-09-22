export PATH="$FLAVOR_DIR/bin:$PATH"
# The flavor is read-only; global installs go to the sandbox's home instead.
export NPM_CONFIG_PREFIX="$HOME/.npm-global"
export PATH="$HOME/.npm-global/bin:$PATH"
