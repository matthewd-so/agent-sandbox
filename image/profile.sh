# Login-shell environment for sandboxes (installed as /etc/profile.d/zz-sbx.sh).
#
# Each flavor may ship an env.sh. They are sourced through /flv/<name>, which
# sbx-init re-points whenever a flavor is updated, so new shells always pick
# up the current version.
if [ -r /etc/sbx/flavors ]; then
  # shellcheck disable=SC2013 # one line, space separated
  for _sbx_f in $(cat /etc/sbx/flavors); do
    if [ -r "/flv/$_sbx_f/env.sh" ]; then
      FLAVOR_DIR="/flv/$_sbx_f"
      # shellcheck source=/dev/null
      . "/flv/$_sbx_f/env.sh"
    fi
  done
  unset _sbx_f FLAVOR_DIR
fi

# Secrets are written by `sbx` at create time (never baked into the image or
# passed on the container command line).
if [ -r "$HOME/.sbx/secrets.env" ]; then
  set -a
  # shellcheck source=/dev/null
  . "$HOME/.sbx/secrets.env"
  set +a
fi

[ -r /etc/sbx/id ] && SBX_ID=$(cat /etc/sbx/id) && export SBX_ID
export PS1='\[\e[36m\]${SBX_ID:-sbx}\[\e[0m\]:\w\$ '
