# sbx — disposable microVM sandboxes for coding agents

Give each coding agent its own throwaway Linux VM instead of your laptop.

```console
$ sbx new --repo https://github.com/you/app --claude
==> starting sandbox sb412 (flavor default: node claude python gh docker default, 4 cpu, 4G)
==> cloning https://github.com/you/app

  sandbox  sb412   (192.168.64.7)
  web      http://sb412.localhost:7777   (any port: http://<port>.sb412.localhost:7777)
  shell    sbx sh sb412
  agent    sbx claude sb412
  remove   sbx rm sb412
```

Agents run with permission prompts off (`--dangerously-skip-permissions`)
because the worst they can do is wreck a VM you were going to delete anyway.

## Why

Running agents directly on your machine, with permission prompts disabled,
risks your files, credentials, and other projects. Git worktrees isolate
code but not processes, ports, or the tools an agent decides to install.
Containers isolate better, but containerized projects then need
Docker-in-Docker and a privileged container.

A microVM per task fixes all of that: each sandbox has its **own kernel**,
so the agent can be root, run Docker, and install anything, and nothing
leaks into your Mac or into other sandboxes. Because sandboxes are
ephemeral, anything worth keeping has to be kept on purpose (pushed,
committed to a flavor), and everything else goes away with `sbx rm`.

The design follows Hanmin Kim's
[*The Agent Sandbox*](https://hanmin.dev/blog/agent-sandbox/), adapted to
run locally on Apple Silicon with Apple's
[`container`](https://github.com/apple/container) runtime instead of Cloud
Hypervisor on a Linux server.

## Features

- **One microVM per sandbox.** Boots in about two seconds, with its own
  kernel, CPU/memory limits, and IP.
- **Flavors.** Composable, read-only tool layers (Node, Claude Code,
  Python, Go, gh, Docker, ...) built once and shared by every sandbox, so
  disk is paid once.
- **Live tool updates.** Rebuild a flavor and every running sandbox
  switches to it within seconds, without rebooting. Flavor hooks restart
  daemons such as dockerd.
- **Docker inside the sandbox.** A real dockerd in the VM, no privileged
  host container.
- **Web URLs per sandbox.** `http://sb412.localhost:7777` and
  `http://5173.sb412.localhost:7777` through a local Caddy proxy.
- **Agent history backup.** Claude Code transcripts are archived when a
  sandbox is removed and can be restored into a new one.
- **Credentials stay out of images.** Tokens are copied into the
  sandbox's home at creation. They're never on the VM command line or in
  its metadata.

## Requirements

- Apple Silicon Mac, macOS 26 or later
- [Homebrew](https://brew.sh)

```sh
brew install container jq caddy
git clone https://github.com/matthewd-so/agent-sandbox ~/agent-sandbox
ln -s ~/agent-sandbox/sbx /opt/homebrew/bin/sbx

sbx init     # starts the runtime, builds the base image and default flavors
sbx auth     # Claude token (from `claude setup-token`) or API key, GitHub token
```

## Usage

```sh
sbx new                          # empty sandbox with the default flavor
sbx new --repo <git url>         # clone a repo into /work
sbx new --mount ./project        # share a host folder read-write (less isolated)
sbx new -n api -f node --cpus 2 --memory 2G --port 8080
sbx new --history sb412          # restore an old sandbox's Claude history

sbx ls                           # list sandboxes
sbx sh sb412                     # shell as the `agent` user, in /work
sbx exec sb412 npm test          # run one command
sbx claude sb412                 # Claude Code, permission prompts off
sbx claude sb412 -p "fix the failing test"

sbx url sb412 5173               # http://5173.sb412.localhost:7777
sbx stop sb412 / sbx start sb412
sbx rm sb412                     # backs up agent history, then deletes
```

Inside a sandbox, the agent user has passwordless `sudo`, the project is
in `/work`, and Docker works without `sudo`.

## Flavors

A flavor is a directory in `flavors/`:

```
flavors/claude/
  flavor      DESC="Claude Code"  DEPS="node"
  build.sh    installs into $OUT (runs in a throwaway build VM)
  env.sh      sourced by login shells; $FLAVOR_DIR is the flavor's path
  hooks/up    optional, run as root on boot and after each update
  hooks/down  optional, run as root before shutdown and before each update
```

Each sandbox uses one flavor plus everything it depends on. The built-in
`default` flavor is just `DEPS="claude python gh docker"`. Add your own
flavors in `flavors/`, or keep them elsewhere and point
`SBX_EXTRA_FLAVORS=/path/to/dir` at them.

```sh
sbx flavor ls                    # status of every flavor
sbx flavor build go              # build a flavor and its dependencies
sbx flavor build --refresh claude  # fetch the latest Claude Code, even if nothing changed
sbx flavor build --all
sbx flavor rollback claude       # go back to the previous build
sbx flavor check                 # validate the dependency graph (also run in CI)
sbx flavor gc                    # delete builds no flavor points to
```

Builds are content-addressed. A flavor is rebuilt only when its files,
its dependencies' builds, or the base image change, unless you pass
`--refresh`.

### How live updates work

All builds live in `~/.sbx/pool`, which is mounted read-only into every VM
at `/opt/sbx/pool`:

```
pool/store/claude-1a2b3c4d5e6f/   immutable build output
pool/ptr/claude                   name of the current build
```

Inside the VM, `/flv/claude` is a symlink to the current build, and
`sbx-init` (the VM's first process) polls `ptr/*` every two seconds. When
a pointer changes, it runs the old build's `down` hook, re-points the
symlink, and runs the new build's `up` hook. New shells pick up the new
version immediately. Running processes keep the old version until they
restart.

The pointer files exist because Apple's virtiofs doesn't reliably show
host-side renames or symlink swaps to the guest, while new paths and
in-place writes do show up. So builds always go to new directories, and
only the guest-side symlink is ever swapped.

## Security model

- The boundary is the VM. Root inside a sandbox has all capabilities, but
  only over that VM's kernel.
- The flavor pool is read-only inside sandboxes.
- `--mount` gives the agent read-write access to that host folder. Prefer
  `--repo` when you don't need it.
- Sandboxes have outbound network access, the same as your Mac. Each one
  gets the credentials you stored with `sbx auth`, so use scoped tokens
  (for example, a fine-grained GitHub token limited to the repos you work on).
- The web proxy listens only on `127.0.0.1`.

## Configuration

| Variable              | Default          |                                    |
| --------------------- | ---------------- | ---------------------------------- |
| `SBX_HOME`            | `~/.sbx`         | pool, history, secrets, proxy      |
| `SBX_CPUS`            | `4`              | per sandbox                        |
| `SBX_MEMORY`          | `4G`             | per sandbox                        |
| `SBX_DEFAULT_FLAVOR`  | `default`        |                                    |
| `SBX_DEFAULT_PORT`    | `3000`           | where `http://<id>.localhost` goes |
| `SBX_PROXY_PORT`      | `7777`           |                                    |
| `SBX_PROXY_AUTO`      | `1`              | start the proxy on first `sbx new` |
| `SBX_EXTRA_FLAVORS`   |                  | extra flavor directories (`:`-separated) |

## Not (yet) here

Compared with the setup in the original post: no memory ballooning or CPU
overcommit (the `container` runtime sizes VMs up front), no GUI desktop or
computer-use integration, and no remote host. The CLI is plain bash with no
daemon, so porting the same layout to Cloud Hypervisor on a Linux box is
mostly a matter of replacing the `container` calls.

## License

MIT
