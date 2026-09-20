# shellcheck shell=bash
# Shared paths and helpers.

SBX_HOME="${SBX_HOME:-$HOME/.sbx}"
SBX_IMAGE="${SBX_IMAGE:-sbx-base:latest}"
SBX_POOL="$SBX_HOME/pool"
SBX_FLAVOR_DIRS="${SBX_FLAVOR_DIRS:-$SBX_ROOT/flavors${SBX_EXTRA_FLAVORS:+:$SBX_EXTRA_FLAVORS}}"
SBX_PREFIX="sbx-"   # container name prefix

if [ -t 2 ]; then
  _c_dim=$'\e[2m' _c_red=$'\e[31m' _c_grn=$'\e[32m' _c_yel=$'\e[33m' _c_off=$'\e[0m'
else
  _c_dim='' _c_red='' _c_grn='' _c_yel='' _c_off=''
fi

info() { printf '%s==>%s %s\n' "$_c_grn" "$_c_off" "$*" >&2; }
warn() { printf '%swarning:%s %s\n' "$_c_yel" "$_c_off" "$*" >&2; }
die()  { printf '%serror:%s %s\n' "$_c_red" "$_c_off" "$*" >&2; exit 1; }
dim()  { printf '%s%s%s\n' "$_c_dim" "$*" "$_c_off" >&2; }

need() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || die "'$c' is required (try: brew install $c)"
  done
}

ensure_dirs() {
  mkdir -p "$SBX_POOL/store" "$SBX_POOL/ptr" "$SBX_HOME/history" "$SBX_HOME/proxy"
}

sha() { shasum -a 256 | cut -c1-"${1:-64}"; }

# All sandboxes as JSON objects, one per line.
sandboxes_json() {
  container ls -a --format json 2>/dev/null |
    jq -c '.[] | select(.configuration.labels["sbx"] == "1") | {
      id: .configuration.labels["sbx.id"],
      name: .id,
      state: .status.state,
      ip: (((.status.networks // [])[0].ipv4Address // "") | split("/")[0] // ""),
      flavor: .configuration.labels["sbx.flavor"],
      port: .configuration.labels["sbx.port"],
      created: .configuration.creationDate,
      cpus: .configuration.resources.cpus,
      mem: .configuration.resources.memoryInBytes
    }'
}

sandbox_field() { # id field
  sandboxes_json | jq -r --arg id "$1" --arg f "$2" 'select(.id == $id) | .[$f] // empty'
}

require_sandbox() {
  local state
  state=$(sandbox_field "$1" state)
  [ -n "$state" ] || die "no such sandbox: $1 (see 'sbx ls')"
  printf '%s' "$state"
}

require_running() {
  local state
  state=$(require_sandbox "$1")
  [ "$state" = running ] || die "sandbox $1 is $state (try 'sbx start $1')"
}
