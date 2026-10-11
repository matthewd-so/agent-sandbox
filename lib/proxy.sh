# shellcheck shell=bash
# Per-sandbox web addresses, served by a local Caddy reverse proxy:
#
#   http://sb123.localhost:7777         -> sandbox sb123, its default port
#   http://5173.sb123.localhost:7777    -> sandbox sb123, port 5173
#
# Browsers and curl resolve *.localhost to 127.0.0.1 without DNS setup. The
# proxy only listens on 127.0.0.1.

SBX_PROXY_PORT="${SBX_PROXY_PORT:-7777}"
SBX_PROXY_ADMIN="${SBX_PROXY_ADMIN:-127.0.0.1:2999}"
SBX_PROXY_AUTO="${SBX_PROXY_AUTO:-1}"

sandbox_url() { printf 'http://%s.localhost:%s' "$1" "$SBX_PROXY_PORT"; }

proxy_running() { curl -fs -m1 "http://$SBX_PROXY_ADMIN/config/" >/dev/null 2>&1; }

proxy_config() {
  local p=$SBX_PROXY_PORT
  cat <<EOF
{
	admin $SBX_PROXY_ADMIN
	auto_https off
	persist_config off
}

EOF
  sandboxes_json | jq -r 'select(.state == "running" and .ip != "") | [.id, .ip, (.port // "3000")] | @tsv' |
    while IFS=$'\t' read -r id ip dport; do
      cat <<EOF
http://$id.localhost:$p {
	bind 127.0.0.1
	reverse_proxy $ip:$dport
}

http://*.$id.localhost:$p {
	bind 127.0.0.1
	@port header_regexp port Host ^([0-9]{1,5})\\.$id\\.localhost(:[0-9]+)?\$
	handle @port {
		reverse_proxy $ip:{re.port.1}
	}
	handle {
		respond "use http://<port>.$id.localhost:$p" 404
	}
}

EOF
    done
  cat <<EOF
http://:$p {
	bind 127.0.0.1
	respond "no such sandbox (see 'sbx ls')" 404
}
EOF
}

proxy_write() {
  ensure_dirs
  proxy_config > "$SBX_HOME/proxy/Caddyfile"
}

proxy_refresh() {
  if proxy_running; then
    proxy_write
    caddy reload --config "$SBX_HOME/proxy/Caddyfile" --adapter caddyfile \
      --address "$SBX_PROXY_ADMIN" >/dev/null 2>&1 || warn "proxy reload failed (see 'sbx proxy status')"
  elif [ "$SBX_PROXY_AUTO" = 1 ] && command -v caddy >/dev/null; then
    proxy_start
  fi
}

proxy_start() {
  need caddy
  proxy_write
  caddy start --config "$SBX_HOME/proxy/Caddyfile" --adapter caddyfile \
    --pidfile "$SBX_HOME/proxy/caddy.pid" > "$SBX_HOME/proxy/caddy.log" 2>&1 ||
    die "proxy failed to start (see $SBX_HOME/proxy/caddy.log)"
}

## Manage the local web proxy: proxy start | stop | status | config
cmd_proxy() {
  case "${1:-status}" in
    start) if proxy_running; then proxy_refresh; else proxy_start; fi
           info "proxy listening on 127.0.0.1:$SBX_PROXY_PORT" ;;
    stop) caddy stop --address "$SBX_PROXY_ADMIN" >/dev/null 2>&1 || true; info "proxy stopped" ;;
    status) if proxy_running; then info "proxy running on 127.0.0.1:$SBX_PROXY_PORT"; else info "proxy not running"; fi ;;
    config) proxy_config ;;
    *) die "usage: sbx proxy [start | stop | status | config]" ;;
  esac
}

## Print the web address of a sandbox: url <id> [port]
cmd_url() {
  local id=${1:?usage: sbx url <id> [port]}
  require_sandbox "$id" >/dev/null
  if [ -n "${2:-}" ]; then
    printf 'http://%s.%s.localhost:%s\n' "$2" "$id" "$SBX_PROXY_PORT"
  else
    printf '%s\n' "$(sandbox_url "$id")"
  fi
}
