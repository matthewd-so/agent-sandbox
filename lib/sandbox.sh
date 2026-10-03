# shellcheck shell=bash
# Sandbox lifecycle. Every sandbox is one microVM (one container under
# Apple's `container` runtime, each with its own kernel). All state about it
# lives in the container's labels, so there's nothing on the host to drift.

SBX_CPUS="${SBX_CPUS:-4}"
SBX_MEMORY="${SBX_MEMORY:-4G}"
SBX_DEFAULT_FLAVOR="${SBX_DEFAULT_FLAVOR:-default}"
SBX_DEFAULT_PORT="${SBX_DEFAULT_PORT:-3000}"

new_id() {
  local id taken
  taken=$(sandboxes_json | jq -r .id)
  while :; do
    id="sb$((RANDOM % 900 + 100))"
    grep -qx "$id" <<<"$taken" || { printf '%s' "$id"; return; }
  done
}

# Run a command in a sandbox as the agent user, in a login shell.
as_agent() { # id [container-exec flags...] -- cmd...
  local id=$1; shift
  local flags=()
  while [ $# -gt 0 ] && [ "$1" != -- ]; do flags+=("$1"); shift; done
  [ "${1:-}" = -- ] && shift
  container exec ${flags[@]+"${flags[@]}"} -u agent -e HOME=/home/agent -e USER=agent \
    -e TERM="${TERM:-xterm-256color}" -w /work "$SBX_PREFIX$id" bash -lc "$1" "${@:2}"
}

as_root() { local id=$1; shift; container exec "$SBX_PREFIX$id" "$@"; }

wait_ready() {
  local id=$1 i
  for i in $(seq 60); do
    as_root "$id" test -f /run/sbx/ready 2>/dev/null && return 0
    sleep 0.5
  done
  die "sandbox $id did not become ready (see 'sbx logs $id')"
}

# First-boot setup inside the sandbox: secrets, git identity, Claude config.
provision() {
  local id=$1 repo=$2 gname gemail
  gname=$(git config --global user.name 2>/dev/null || true)
  gemail=$(git config --global user.email 2>/dev/null || true)

  if [ -f "$SBX_HOME/secrets.env" ]; then
    as_agent "$id" -i -- 'mkdir -p ~/.sbx && umask 077 && cat > ~/.sbx/secrets.env' < "$SBX_HOME/secrets.env"
  fi

  as_agent "$id" -e "GIT_NAME=$gname" -e "GIT_EMAIL=$gemail" -- '
    set -e
    [ -n "$GIT_NAME" ] && git config --global user.name "$GIT_NAME"
    [ -n "$GIT_EMAIL" ] && git config --global user.email "$GIT_EMAIL"
    git config --global init.defaultBranch main
    git config --global --add safe.directory /work
    # HTTPS pushes to GitHub use $GH_TOKEN from ~/.sbx/secrets.env, if set.
    git config --global credential.https://github.com.helper \
      "!f() { test \"\$1\" = get && test -n \"\$GH_TOKEN\" && printf \"username=x-access-token\npassword=%s\n\" \"\$GH_TOKEN\"; }; f"
    # Skip Claude Code first-run prompts; the sandbox is the permission boundary.
    cat > ~/.claude.json <<JSON
{"hasCompletedOnboarding": true, "bypassPermissionsModeAccepted": true,
 "projects": {"/work": {"hasTrustDialogAccepted": true}}}
JSON
  '

  if [ -n "$repo" ]; then
    info "cloning $repo"
    as_agent "$id" -e "REPO=$repo" -- 'git clone "$REPO" /work' ||
      warn "clone failed; /work is empty"
  fi
}

## Create a sandbox: [-f flavor] [-n name] [--repo url] [--mount dir] [--cpus n] [--memory 4G] [--port 3000] [--claude]
cmd_new() {
  need container jq
  ensure_dirs
  local flavor=$SBX_DEFAULT_FLAVOR id="" repo="" mount="" cpus=$SBX_CPUS mem=$SBX_MEMORY
  local port=$SBX_DEFAULT_PORT history="" attach=0
  while [ $# -gt 0 ]; do
    case "$1" in
      -f|--flavor) flavor=$2; shift ;;
      -n|--name) id=$2; shift ;;
      --repo) repo=$2; shift ;;
      --mount) mount=$(cd "$2" && pwd) || die "no such directory: $2"; shift ;;
      --cpus) cpus=$2; shift ;;
      --memory) mem=$2; shift ;;
      --port) port=$2; shift ;;
      --claude) attach=1 ;;
      *) die "unknown option: $1" ;;
    esac
    shift
  done
  [ -n "$repo" ] && [ -n "$mount" ] && die "use --repo or --mount, not both"
  [ -n "$id" ] || id=$(new_id)
  case "$id" in *[!a-z0-9-]*|'') die "sandbox names must be lowercase letters, digits and dashes" ;; esac
  [ -z "$(sandbox_field "$id" state)" ] || die "sandbox $id already exists"

  local closure
  closure=$(flavor_closure "$flavor" | tr '\n' ' ')
  local f
  for f in $closure; do
    if [ "$(flavor_status "$f")" = unbuilt ]; then
      info "flavor $f is not built yet; building"
      flavor_build 0 "$flavor"
      break
    fi
  done

  local mounts=(--mount "type=bind,source=$SBX_POOL,target=/opt/sbx/pool,readonly")
  if [ -n "$mount" ]; then
    warn "mounting $mount read-write: the agent can change those files directly"
    mounts+=(--mount "type=bind,source=$mount,target=/work")
  fi

  info "starting sandbox $id (flavor $flavor: ${closure% }, ${cpus} cpu, $mem)"
  # Root inside the VM gets all capabilities so Docker etc. work. That root
  # is confined to the VM's own kernel, not the Mac's.
  # Don't leave a half-provisioned sandbox behind if anything below fails.
  # shellcheck disable=SC2064
  trap "container rm -f '$SBX_PREFIX$id' >/dev/null 2>&1" EXIT
  container run -d --progress none --name "$SBX_PREFIX$id" \
    --cpus "$cpus" --memory "$mem" --cap-add ALL --init \
    -l sbx=1 -l "sbx.id=$id" -l "sbx.flavor=$flavor" -l "sbx.port=$port" \
    -e "SBX_ID=$id" -e "SBX_FLAVORS=${closure% }" \
    "${mounts[@]}" "$SBX_IMAGE" >/dev/null

  wait_ready "$id"
  provision "$id" "$repo"
  trap - EXIT

  local ip
  ip=$(sandbox_field "$id" ip)
  cat >&2 <<EOF

  sandbox  $id   ($ip)
  shell    sbx sh $id
  agent    sbx claude $id
  remove   sbx rm $id

EOF
  printf '%s\n' "$id"
  if [ "$attach" = 1 ]; then cmd_claude "$id"; fi
}

## List sandboxes
cmd_ls() {
  need container jq
  printf '%-10s %-9s %-10s %-15s %-5s %-6s %s\n' ID STATE FLAVOR IP CPUS MEM CREATED
  sandboxes_json | jq -r '[.id, .state, .flavor, (if .ip == "" then "-" else .ip end),
      (.cpus|tostring), ((.mem/1073741824|floor|tostring)+"G"), (.created // "" | sub("T"; " ") | sub("Z$"; ""))] | @tsv' |
    while IFS=$'\t' read -r a b c d e f g; do
      printf '%-10s %-9s %-10s %-15s %-5s %-6s %s\n' "$a" "$b" "$c" "$d" "$e" "$f" "$g"
    done
}

## Open a shell in a sandbox (as the agent user)
cmd_sh() {
  local id=${1:?usage: sbx sh <id>}
  require_running "$id"
  as_agent "$id" -it -- 'exec bash -l'
}

## Run a command in a sandbox: exec <id> <cmd...>
cmd_exec() {
  local id=${1:?usage: sbx exec <id> <cmd...>}; shift
  [ $# -gt 0 ] || die "usage: sbx exec <id> <cmd...>"
  require_running "$id"
  local tty=()
  [ -t 0 ] && [ -t 1 ] && tty=(-it)
  as_agent "$id" ${tty[@]+"${tty[@]}"} -- '"$@"' bash "$@"
}

## Start a stopped sandbox
cmd_start() {
  local id
  for id in "$@"; do
    require_sandbox "$id" >/dev/null
    container start "$SBX_PREFIX$id" >/dev/null
    wait_ready "$id"
    info "started $id"
  done
}

stop_one() {
  # SIGTERM lets sbx-init run each flavor's down hook (e.g. stop dockerd)
  # before the VM goes away.
  container stop -t 20 "$SBX_PREFIX$1" >/dev/null 2>&1 ||
    container kill "$SBX_PREFIX$1" >/dev/null 2>&1 || true
}

## Stop sandboxes without deleting them
cmd_stop() {
  local id
  for id in "$@"; do
    require_sandbox "$id" >/dev/null
    stop_one "$id"
    info "stopped $id"
  done
}

## Delete sandboxes
cmd_rm() {
  local ids=() id state
  for id in "$@"; do
    case "$id" in -*) die "unknown option: $id" ;; *) ids+=("$id") ;; esac
  done
  [ ${#ids[@]} -gt 0 ] || die "usage: sbx rm <id...>"
  for id in "${ids[@]}"; do
    state=$(require_sandbox "$id")
    [ "$state" = running ] && stop_one "$id"
    for _ in 1 2 3 4 5; do
      container rm -f "$SBX_PREFIX$id" >/dev/null 2>&1 && break
      container kill "$SBX_PREFIX$id" >/dev/null 2>&1 || true
      sleep 1
    done
    [ -z "$(sandbox_field "$id" state)" ] || die "could not remove $id"
    info "removed $id"
  done
}

## Show a sandbox's init and flavor hook logs
cmd_logs() {
  local id=${1:?usage: sbx logs <id>}
  require_running "$id"
  as_root "$id" sh -c 'for f in /var/log/sbx/*.log; do echo "== $f"; tail -n 50 "$f"; done'
}
