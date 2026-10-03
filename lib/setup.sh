# shellcheck shell=bash
# One-time setup and the base image.

## Build the base VM image
cmd_image() {
  need container
  info "building $SBX_IMAGE"
  container build --progress plain -t "$SBX_IMAGE" "$SBX_ROOT/image"
}

## First-time setup: start the runtime, build the image and default flavors
cmd_init() {
  need container jq
  ensure_dirs
  if ! container system status >/dev/null 2>&1; then
    info "starting the container runtime"
    container system start --enable-kernel-install
  fi
  if ! container image inspect "$SBX_IMAGE" >/dev/null 2>&1; then
    cmd_image
  fi
  info "building flavors for '$SBX_DEFAULT_FLAVOR'"
  flavor_build 0 "$SBX_DEFAULT_FLAVOR"
  [ -f "$SBX_HOME/secrets.env" ] || dim "    next: 'sbx auth' to give sandboxes Claude/GitHub credentials"
  info "ready. Try: sbx new --repo https://github.com/you/project"
}
