# shellcheck shell=bash
# Flavors: composable, read-only tool layers shared by every sandbox.
#
# A flavor is a directory containing:
#   flavor       sourced shell: DESC="..." and DEPS="other flavors"
#   build.sh     optional; runs in a throwaway VM and installs into $OUT
#   env.sh       optional; sourced by login shells (FLAVOR_DIR is set)
#   hooks/up     optional; run as root when a sandbox starts or the flavor updates
#   hooks/down   optional; run as root before shutdown or before an update
#
# Builds are content-addressed and immutable: store/<name>-<id>. Publishing a
# build rewrites ptr/<name> in place, and every running sandbox re-points
# /flv/<name> at it within a couple of seconds (see image/sbx-init).

flavor_dir() {
  local d IFS=:
  for d in $SBX_FLAVOR_DIRS; do
    if [ -f "$d/$1/flavor" ]; then printf '%s' "$d/$1"; return 0; fi
  done
  return 1
}

flavor_names() {
  local d f IFS=:
  for d in $SBX_FLAVOR_DIRS; do
    for f in "$d"/*/flavor; do
      [ -f "$f" ] && basename "$(dirname "$f")"
    done
  done | LC_ALL=C sort -u
}

flavor_var() { # name VAR
  local dir
  dir=$(flavor_dir "$1") || die "unknown flavor: $1"
  # shellcheck disable=SC1091
  ( DESC=""; DEPS=""; . "$dir/flavor"; eval "printf '%s' \"\${$2}\"" )
}

flavor_deps() { flavor_var "$1" DEPS; }

# Dependency closure of one or more flavors, dependencies first.
flavor_closure() {
  _fc_done="" _fc_path=""
  local n
  for n in "$@"; do _fc_visit "$n"; done
  # shellcheck disable=SC2086 # intentional word splitting
  printf '%s\n' $_fc_done
}

_fc_visit() {
  local n=$1 d deps
  case " $_fc_done " in *" $n "*) return 0 ;; esac
  case " $_fc_path " in *" $n "*) die "flavor dependency cycle: ${_fc_path}-> $n" ;; esac
  flavor_dir "$n" >/dev/null || die "unknown flavor: '$n'${_fc_path:+ (needed by ${_fc_path% })}"
  _fc_path="$_fc_path$n "
  deps=$(flavor_deps "$n")
  for d in $deps; do _fc_visit "$d"; done
  _fc_path="${_fc_path%"$n "}"
  _fc_done="$_fc_done $n"
}

flavor_current() { head -n1 "$SBX_POOL/ptr/$1" 2>/dev/null || true; }

flavor_build_src() { # build -> source hash recorded at build time
  sed -n 's/^src=//p' "$SBX_POOL/store/$1/.sbx-build" 2>/dev/null || true
}

# Hash of everything that determines a build: the flavor's files, the
# current builds of its direct dependencies, and the base image definition.
flavor_srchash() {
  local name=$1 dir d deps
  dir=$(flavor_dir "$name")
  deps=$(flavor_deps "$name")
  {
    (cd "$dir" && find . -type f ! -name '.*' | LC_ALL=C sort | while read -r f; do
      printf '%s %s\n' "$f" "$(sha < "$f")"
    done)
    for d in $deps; do printf 'dep %s %s\n' "$d" "$(flavor_current "$d")"; done
    printf 'image %s\n' "$(sha < "$SBX_ROOT/image/Containerfile")"
  } | sha
}

flavor_status() {
  local name=$1 cur
  cur=$(flavor_current "$name")
  if [ -z "$cur" ]; then echo unbuilt
  elif [ "$(flavor_build_src "$cur")" = "$(flavor_srchash "$name")" ]; then echo ok
  else echo stale
  fi
}

flavor_publish() { # name build
  # In-place write (same inode) so the change is visible inside running VMs.
  printf '%s\n' "$2" > "$SBX_POOL/ptr/$1"
  info "published $2"
}

flavor_build_one() { # name refresh(0/1)
  local name=$1 refresh=${2:-0} dir src cur salt="" id build tmp deps d
  dir=$(flavor_dir "$name")
  src=$(flavor_srchash "$name")
  cur=$(flavor_current "$name")

  if [ "$refresh" = 0 ] && [ -n "$cur" ] && [ "$(flavor_build_src "$cur")" = "$src" ]; then
    dim "    $name is up to date ($cur)"
    return 0
  fi
  [ "$refresh" = 1 ] && salt=$(date +%s)
  id=$(printf '%s%s' "$src" "$salt" | sha 12)
  build="$name-$id"

  if [ -d "$SBX_POOL/store/$build" ]; then
    flavor_publish "$name" "$build"
    return 0
  fi

  info "building flavor $name -> $build"
  deps=$(flavor_closure "$name" | { grep -vx "$name" || true; } | tr '\n' ' ')
  tmp="$SBX_POOL/store/.tmp-$build-$$"
  rm -rf "$tmp"; mkdir -p "$tmp"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT

  local mounts=(--mount "type=bind,source=$dir,target=/src,readonly")
  for d in $deps; do
    [ -n "$(flavor_current "$d")" ] || die "dependency $d of $name is not built"
    mounts+=(--mount "type=bind,source=$SBX_POOL/store/$(flavor_current "$d"),target=/flv/$d,readonly")
  done

  # The build runs on the builder VM's own disk at the same path sandboxes
  # will see (/flv/<name>), so absolute paths baked in by installers stay
  # valid. The result streams back as a tar archive on stdout; build output
  # goes to stderr. (Writing straight into a virtiofs mount is unreliable.)
  if [ -f "$dir/build.sh" ]; then
    rt_run --rm --name "sbx-build-$name-$$" \
      --cpus "${SBX_BUILD_CPUS:-4}" --memory "${SBX_BUILD_MEMORY:-4G}" \
      "${mounts[@]}" \
      -e "OUT=/flv/$name" -e "FLAVOR=$name" -e "DEPS=$deps" -e HOME=/root \
      --entrypoint /bin/bash "$SBX_IMAGE" -c '
        set -euo pipefail
        exec 3>&1 1>&2
        for d in $DEPS; do
          if [ -r "/flv/$d/env.sh" ]; then FLAVOR_DIR="/flv/$d"; . "/flv/$d/env.sh"; fi
        done
        mkdir -p "$OUT" && cd "$OUT"
        bash -euo pipefail /src/build.sh
        tar -C "$OUT" -cf - . >&3
      ' | tar -xf - -C "$tmp" || die "build of $name failed"
  fi

  [ -f "$dir/env.sh" ] && cp "$dir/env.sh" "$tmp/env.sh"
  if [ -d "$dir/hooks" ]; then
    mkdir -p "$tmp/hooks"
    cp "$dir"/hooks/* "$tmp/hooks/"
    chmod +x "$tmp"/hooks/*
  fi
  {
    printf 'src=%s\n' "$src"
    printf 'built=%s\n' "$(date -u +%FT%TZ)"
    printf 'deps=%s\n' "$deps"
  } > "$tmp/.sbx-build"

  mv "$tmp" "$SBX_POOL/store/$build"
  trap - EXIT
  flavor_publish "$name" "$build"
}

flavor_build() { # refresh names...
  local refresh=$1 n targets
  shift
  targets=$(flavor_closure "$@")
  for n in $targets; do
    # --refresh only re-fetches the flavors named; dependents rebuild
    # automatically because their hash includes the dependency's build id.
    case " $* " in *" $n "*) flavor_build_one "$n" "$refresh" ;; *) flavor_build_one "$n" 0 ;; esac
  done
}

flavor_check() {
  local n errs=0 dir h
  for n in $(flavor_names); do
    dir=$(flavor_dir "$n")
    if ! (flavor_closure "$n" >/dev/null); then errs=$((errs+1)); continue; fi
    for h in "$dir"/hooks/*; do
      [ -e "$h" ] || continue
      case "${h##*/}" in up|down) ;; *) warn "$n: unknown hook ${h##*/}"; errs=$((errs+1)) ;; esac
    done
    if [ -f "$dir/build.sh" ] && ! bash -n "$dir/build.sh"; then errs=$((errs+1)); fi
    if [ -f "$dir/env.sh" ] && ! bash -n "$dir/env.sh"; then errs=$((errs+1)); fi
    [ -n "$(flavor_var "$n" DESC)" ] || warn "$n: no DESC"
  done
  [ "$errs" = 0 ] || die "$errs flavor problem(s)"
  info "$(flavor_names | wc -l | tr -d ' ') flavors OK"
}

flavor_gc() {
  local b n keep="" removed=0
  for n in $(flavor_names); do keep="$keep $(flavor_current "$n")"; done
  for b in "$SBX_POOL"/store/*; do
    [ -d "$b" ] || continue
    case " $keep " in *" ${b##*/} "*) continue ;; esac
    rm -rf "$b"; removed=$((removed+1))
    dim "    removed ${b##*/}"
  done
  rm -rf "$SBX_POOL"/store/.tmp-*
  info "removed $removed old build(s)"
}

## Manage flavors: ls | build [--refresh] [names|--all] | check | gc | rollback
cmd_flavor() {
  ensure_dirs
  local sub="${1:-ls}"
  [ $# -gt 0 ] && shift
  case "$sub" in
    ls|list)
      printf '%-10s %-8s %-22s %-24s %s\n' FLAVOR STATUS BUILD DEPS DESCRIPTION
      local n cur deps
      for n in $(flavor_names); do
        cur=$(flavor_current "$n"); deps=$(flavor_deps "$n")
        printf '%-10s %-8s %-22s %-24s %s\n' "$n" "$(flavor_status "$n")" \
          "${cur:--}" "${deps:--}" "$(flavor_var "$n" DESC)"
      done ;;
    build)
      need "$SBX_RUNTIME"
      local refresh=0 names=() n
      while [ $# -gt 0 ]; do
        case "$1" in
          --refresh) refresh=1 ;;
          --all) while read -r n; do names+=("$n"); done < <(flavor_names) ;;
          -*) die "unknown option: $1" ;;
          *) names+=("$1") ;;
        esac
        shift
      done
      [ ${#names[@]} -gt 0 ] || die "usage: sbx flavor build [--refresh] <name...|--all>"
      flavor_build "$refresh" "${names[@]}" ;;
    check) flavor_check ;;
    gc) flavor_gc ;;
    rollback)
      local name=${1:-} prev
      [ -n "$name" ] || die "usage: sbx flavor rollback <name>"
      local b cur
      cur=$(flavor_current "$name") prev=""
      for b in "$SBX_POOL/store/$name"-*; do   # newest other build wins
        [ -d "$b" ] && [ "${b##*/}" != "$cur" ] || continue
        if [ -z "$prev" ] || [ "$b" -nt "$SBX_POOL/store/$prev" ]; then prev=${b##*/}; fi
      done
      [ -n "$prev" ] || die "no previous build of $name"
      flavor_publish "$name" "$prev" ;;
    *) die "unknown flavor subcommand: $sub" ;;
  esac
}
