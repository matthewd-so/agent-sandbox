# shellcheck shell=bash
# Running agents inside sandboxes, and the credentials they get.

## Store credentials passed to new sandboxes (Claude token/API key, GitHub token)
cmd_auth() {
  ensure_dirs
  local file="$SBX_HOME/secrets.env" key val
  if [ "${1:-}" = --show ]; then
    [ -f "$file" ] || { echo "no credentials stored"; return; }
    sed 's/=.*/=<set>/' "$file"
    return
  fi

  echo "Credentials are stored in $file (mode 600) and copied into each new"
  echo "sandbox's home directory. Leave a value blank to keep it unchanged."
  echo
  echo "Claude Code: run 'claude setup-token' on this Mac for a long-lived token,"
  echo "or use an API key from console.anthropic.com."

  local tmp
  tmp=$(mktemp "$SBX_HOME/.secrets.XXXXXX")
  chmod 600 "$tmp"
  [ -f "$file" ] && cat "$file" > "$tmp"

  for key in CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_API_KEY GH_TOKEN; do
    if [ "$key" = GH_TOKEN ] && command -v gh >/dev/null && gh auth token >/dev/null 2>&1; then
      printf 'GH_TOKEN: use the token from your gh login? [y/N] '
      read -r val
      if [ "$val" = y ] || [ "$val" = Y ]; then
        val=$(gh auth token)
        grep -v "^$key=" "$tmp" > "$tmp.n" || true; mv "$tmp.n" "$tmp"
        printf '%s=%s\n' "$key" "$val" >> "$tmp"
        continue
      fi
    fi
    printf '%s: ' "$key"
    read -rs val; echo
    [ -n "$val" ] || continue
    grep -v "^$key=" "$tmp" > "$tmp.n" || true; mv "$tmp.n" "$tmp"
    printf '%s=%s\n' "$key" "$val" >> "$tmp"
  done
  chmod 600 "$tmp"
  mv "$tmp" "$file"
  info "saved. New sandboxes get these; for existing ones, recreate them."
}

## Run Claude Code in a sandbox with permission prompts off: claude <id> [args]
cmd_claude() {
  local id=${1:?usage: sbx claude <id> [claude args...]}; shift
  require_running "$id"
  as_agent "$id" -it -- 'exec claude --dangerously-skip-permissions "$@"' claude "$@"
}
