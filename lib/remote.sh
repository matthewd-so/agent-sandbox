# shellcheck shell=bash
# Remote sandboxes. With SBX_REMOTE=user@server, sandbox commands are run by
# sbx-host (cmd/sbx-host) on that server, where each sandbox is a Cloud
# Hypervisor microVM. Flavors, images and builds are managed on the server
# itself; see scripts/ch-host-setup.sh.

SBX_REMOTE="${SBX_REMOTE:-}"
SBX_REMOTE_CMDS=" new ls sh exec claude start stop rm resize info logs "

remote_handles() { case "$SBX_REMOTE_CMDS" in *" $1 "*) return 0 ;; esac; return 1; }

remote_dispatch() {
  local cmd=$1; shift
  local args=("$@") tty=() q="" a
  if [ "$cmd" = new ]; then
    args+=(--git-name "$(git config --global user.name 2>/dev/null || true)"
           --git-email "$(git config --global user.email 2>/dev/null || true)")
  fi
  case "$cmd" in
    sh|claude) tty=(-t) ;;
    exec) [ -t 0 ] && [ -t 1 ] && tty=(-t) ;;
  esac
  for a in ${args[@]+"${args[@]}"}; do q="$q $(printf '%q' "$a")"; done
  exec ssh ${tty[@]+"${tty[@]}"} -o LogLevel=ERROR "$SBX_REMOTE" "sudo sbx-host $cmd$q"
}

## Remote host (SBX_REMOTE): remote auth | status
cmd_remote() {
  [ -n "$SBX_REMOTE" ] || die "set SBX_REMOTE=user@server first"
  case "${1:-status}" in
    auth)
      [ -f "$SBX_HOME/secrets.env" ] || die "no local credentials; run 'sbx auth' first"
      ssh -o LogLevel=ERROR "$SBX_REMOTE" "sudo sbx-host auth --stdin" < "$SBX_HOME/secrets.env" ;;
    status)
      ssh -o LogLevel=ERROR "$SBX_REMOTE" "sudo sbx-host init && sudo sbx-host ls" ;;
    *) die "usage: sbx remote [auth | status]" ;;
  esac
}
