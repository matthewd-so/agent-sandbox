# shellcheck shell=bash
# One-time setup and the base image.

## Build the base VM image
cmd_image() {
  need container
  info "building $SBX_IMAGE"
  container build --progress plain -t "$SBX_IMAGE" "$SBX_ROOT/image"
}
