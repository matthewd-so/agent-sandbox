# shellcheck shell=bash
# Agent history backup. A sandbox is disposable, but the agent's
# transcripts are worth keeping: they're archived to ~/.sbx/history when a
# sandbox is removed, and can be restored into a new one with --history.

history_save() {
  local id=$1 out
  out="$SBX_HOME/history/$id-$(date +%Y%m%d-%H%M%S).tgz"
  # Only archive if the agent actually ran.
  as_root "$id" test -d /home/agent/.claude 2>/dev/null || return 0
  if as_root "$id" tar -C /home/agent -czf - .claude .claude.json .bash_history 2>/dev/null > "$out.tmp" ||
     [ -s "$out.tmp" ]; then
    mv "$out.tmp" "$out"
    info "saved agent history to ${out/#$HOME/~}"
  else
    rm -f "$out.tmp"
    warn "could not save history for $id"
    return 1
  fi
}

history_restore() { # id <archive path | sandbox id>
  local id=$1 src=$2
  if [ ! -f "$src" ]; then
    # shellcheck disable=SC2012
    src=$(ls -t "$SBX_HOME/history/$2-"*.tgz 2>/dev/null | head -n1)
    [ -n "$src" ] || die "no saved history for $2 (see 'sbx history')"
  fi
  info "restoring history from ${src/#$HOME/~}"
  container exec -i "$SBX_PREFIX$id" sh -c 'tar -C /home/agent -xzf - && chown -R agent:agent /home/agent' < "$src"
}

## List or save agent history: history [save <id>]
cmd_history() {
  ensure_dirs
  case "${1:-ls}" in
    ls)
      # shellcheck disable=SC2012
      ls -lht "$SBX_HOME/history" | awk 'NR>1 {print $5"\t"$6" "$7" "$8"\t"$9}' ;;
    save)
      require_running "${2:?usage: sbx history save <id>}"
      history_save "$2" ;;
    *) die "usage: sbx history [ls | save <id>]" ;;
  esac
}
