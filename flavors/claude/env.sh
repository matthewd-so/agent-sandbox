export PATH="$FLAVOR_DIR/bin:$PATH"
# Updates arrive by rebuilding the flavor (sbx flavor build --refresh claude),
# not by the CLI rewriting itself (the flavor is read-only anyway).
export DISABLE_AUTOUPDATER=1
alias yolo='claude --dangerously-skip-permissions'
