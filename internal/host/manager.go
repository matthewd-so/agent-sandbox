package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/matthewd-so/agent-sandbox/internal/ch"
	"github.com/matthewd-so/agent-sandbox/internal/netalloc"
)

// Manager creates and controls sandboxes.
type Manager struct {
	Cfg   Config
	Run   Runner
	Pool  netalloc.Pool
	Store Store
	Arch  string        // guest architecture; defaults to the host's
	Log   func(string)  // progress messages
	Wait  time.Duration // poll interval used while waiting on processes
}

// NewManager validates cfg and returns a Manager using r.
func NewManager(cfg Config, r Runner) (*Manager, error) {
	pool, err := netalloc.NewPool(cfg.Subnet)
	if err != nil {
		return nil, err
	}
	return &Manager{Cfg: cfg, Run: r, Pool: pool, Store: Store{cfg}, Log: func(string) {}, Wait: 100 * time.Millisecond}, nil
}

func (m *Manager) logf(format string, a ...any) { m.Log(fmt.Sprintf(format, a...)) }

// CreateOptions are the inputs to Create.
type CreateOptions struct {
	ID       string
	Flavor   string
	Repo     string
	CPUs     int
	Memory   int64
	Port     int
	GitName  string
	GitEmail string
}

// Status is the live state of a sandbox.
type Status struct {
	Running bool
	State   ch.VmState // empty when the VMM is not running
	// MemoryActual is the guest memory currently backed by the host,
	// which shrinks as the balloon inflates.
	MemoryActual int64
}

// Create allocates, boots and provisions a new sandbox.
func (m *Manager) Create(ctx context.Context, o CreateOptions) (*Sandbox, error) {
	if o.Flavor == "" {
		o.Flavor = "default"
	}
	if o.CPUs <= 0 {
		o.CPUs = m.Cfg.CPUs
	}
	if o.Memory <= 0 {
		o.Memory = m.Cfg.Memory
	}
	if o.Port <= 0 {
		o.Port = m.Cfg.Port
	}
	closure, err := Closure(m.Cfg.FlavorDirs, o.Flavor)
	if err != nil {
		return nil, err
	}
	for _, f := range closure {
		if Published(m.Cfg.PoolDir(), f) == "" {
			return nil, fmt.Errorf("flavor %s is not built on this host (run: sbx flavor build %s)", f, o.Flavor)
		}
	}
	if _, err := os.Stat(m.Cfg.BaseImage); err != nil {
		return nil, fmt.Errorf("base image: %w (see scripts/ch-host-setup.sh)", err)
	}

	sb, err := m.reserve(o, closure)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			m.Remove(context.Background(), sb, false)
		}
	}()

	m.logf("preparing disk for %s", sb.ID)
	dir := m.Cfg.SandboxDir(sb.ID)
	// Reflink copies are instant on XFS and btrfs; elsewhere this is a sparse copy.
	if _, err := m.Run.Run(ctx, nil, "cp", "--reflink=auto", "--sparse=always",
		m.Cfg.BaseImage, filepath.Join(dir, "rootfs.ext4")); err != nil {
		return nil, err
	}
	if err := m.writeSeed(sb); err != nil {
		return nil, err
	}
	if err := m.Start(ctx, sb); err != nil {
		return nil, err
	}
	if err := m.provision(ctx, sb, o); err != nil {
		return nil, err
	}
	ok = true
	return sb, nil
}

// reserve picks an id and address under the host lock and persists them,
// so concurrent creates never collide.
func (m *Manager) reserve(o CreateOptions, closure []string) (*Sandbox, error) {
	unlock, err := m.Store.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	existing, err := m.Store.List()
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	var used []netip.Addr
	for _, s := range existing {
		taken[s.ID] = true
		used = append(used, s.IP)
	}
	id := o.ID
	if id == "" {
		for {
			id = fmt.Sprintf("sb%d", 100+rand.Intn(900))
			if !taken[id] {
				break
			}
		}
	}
	if err := ValidID(id); err != nil {
		return nil, err
	}
	if taken[id] {
		return nil, fmt.Errorf("sandbox %s already exists", id)
	}
	ip, err := m.Pool.Allocate(used)
	if err != nil {
		return nil, err
	}
	sb := &Sandbox{
		ID: id, Flavor: o.Flavor, Flavors: closure,
		IP: ip, MAC: netalloc.MAC(ip), Tap: netalloc.TapName(id),
		CPUs: o.CPUs, MaxCPUs: max(o.CPUs, m.Cfg.MaxCPUs), Memory: o.Memory, Port: o.Port,
		Repo: o.Repo, Created: time.Now().UTC(),
	}
	return sb, m.Store.Save(sb)
}

// writeSeed prepares the small read-only share the guest reads at boot:
// the host's SSH public key and, if configured, the agent's credentials.
func (m *Manager) writeSeed(sb *Sandbox) error {
	seed := filepath.Join(m.Cfg.SandboxDir(sb.ID), "seed")
	if err := os.MkdirAll(seed, 0o700); err != nil {
		return err
	}
	pub, err := os.ReadFile(m.Cfg.SSHKey() + ".pub")
	if err != nil {
		return fmt.Errorf("host SSH key missing (run: sbx-host init): %w", err)
	}
	if err := os.WriteFile(filepath.Join(seed, "authorized_keys"), pub, 0o600); err != nil {
		return err
	}
	if sec, err := os.ReadFile(m.Cfg.SecretsFile()); err == nil {
		return os.WriteFile(filepath.Join(seed, "secrets.env"), sec, 0o600)
	}
	return nil
}

func (m *Manager) pidFile(id, name string) string { return m.Cfg.RunPath(id, name+".pid") }

func (m *Manager) readPid(id, name string) int {
	b, err := os.ReadFile(m.pidFile(id, name))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func (m *Manager) client(id string) *ch.Client { return ch.NewClient(m.Cfg.RunPath(id, "api.sock")) }

// Status reports whether the sandbox's VMM is running and its VM state.
func (m *Manager) Status(ctx context.Context, sb *Sandbox) Status {
	if !m.Run.Alive(m.readPid(sb.ID, "ch")) {
		return Status{}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	info, err := m.client(sb.ID).Info(ctx)
	if err != nil {
		return Status{Running: true}
	}
	return Status{Running: info.State == ch.StateRunning, State: info.State, MemoryActual: info.MemoryActualSize}
}

// Start brings up networking, the virtiofs daemons and the VM.
func (m *Manager) Start(ctx context.Context, sb *Sandbox) error {
	if m.Status(ctx, sb).Running {
		return nil
	}
	run := filepath.Join(m.Cfg.RunDir, sb.ID)
	if err := os.MkdirAll(run, 0o700); err != nil {
		return err
	}
	m.cleanupProcesses(sb) // stale state from a crash or reboot

	m.logf("starting %s (%s, %d vCPU, %s)", sb.ID, sb.IP, sb.CPUs, FormatSize(sb.Memory))
	if err := m.setupTap(ctx, sb); err != nil {
		return err
	}
	shares := []struct{ name, dir string }{
		{"pool", m.Cfg.PoolDir()},
		{"seed", filepath.Join(m.Cfg.SandboxDir(sb.ID), "seed")},
	}
	for _, s := range shares {
		sock := m.Cfg.RunPath(sb.ID, s.name+".sock")
		os.Remove(sock)
		pid, err := m.Run.Start(m.Cfg.Virtiofsd, []string{
			"--socket-path", sock, "--shared-dir", s.dir, "--readonly",
			"--cache", "auto", "--sandbox", "chroot",
		}, m.Cfg.RunPath(sb.ID, "virtiofsd-"+s.name+".log"))
		if err != nil {
			return err
		}
		if err := m.writePid(sb.ID, "virtiofsd-"+s.name, pid); err != nil {
			return err
		}
		if err := m.waitFile(ctx, sock, 5*time.Second); err != nil {
			return fmt.Errorf("virtiofsd (%s): %w", s.name, err)
		}
	}

	api := m.Cfg.RunPath(sb.ID, "api.sock")
	os.Remove(api)
	pid, err := m.Run.Start(m.Cfg.CloudHypervisor, []string{"--api-socket", "path=" + api},
		filepath.Join(m.Cfg.SandboxDir(sb.ID), "vmm.log"))
	if err != nil {
		return err
	}
	if err := m.writePid(sb.ID, "ch", pid); err != nil {
		return err
	}
	c := m.client(sb.ID)
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.WaitReady(readyCtx); err != nil {
		return err
	}
	if err := c.Create(ctx, m.VMConfig(sb)); err != nil {
		return err
	}
	return c.Boot(ctx)
}

func (m *Manager) writePid(id, name string, pid int) error {
	return os.WriteFile(m.pidFile(id, name), []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func (m *Manager) waitFile(ctx context.Context, path string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not appear", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.Wait / 4):
		}
	}
}

func (m *Manager) ip(ctx context.Context, args ...string) error {
	_, err := m.Run.Run(ctx, nil, "ip", args...)
	return err
}

func (m *Manager) setupTap(ctx context.Context, sb *Sandbox) error {
	m.ip(ctx, "link", "del", sb.Tap) // ignore: usually absent
	steps := [][]string{
		{"tuntap", "add", "dev", sb.Tap, "mode", "tap"},
		{"link", "set", "dev", sb.Tap, "master", m.Cfg.Bridge},
		// Isolated bridge ports cannot talk to each other, only to the uplink,
		// so sandboxes cannot reach one another.
		{"link", "set", "dev", sb.Tap, "type", "bridge_slave", "isolated", "on"},
		{"link", "set", "dev", sb.Tap, "up"},
	}
	for _, s := range steps {
		if err := m.ip(ctx, s...); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) ssh(ctx context.Context, sb *Sandbox, stdin []byte, script string) ([]byte, error) {
	var in *bytes.Reader
	if stdin != nil {
		in = bytes.NewReader(stdin)
	}
	args := append(m.SSHArgs(sb, false), script)
	if in == nil {
		return m.Run.Run(ctx, nil, "ssh", args...)
	}
	return m.Run.Run(ctx, in, "ssh", args...)
}

// SSHArgs returns the ssh arguments for reaching the agent user in sb.
func (m *Manager) SSHArgs(sb *Sandbox, tty bool) []string {
	args := []string{
		"-i", m.Cfg.SSHKey(),
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", "-o", "ConnectTimeout=5",
	}
	if tty {
		args = append(args, "-t")
	}
	return append(args, "agent@"+sb.IP.String())
}

func (m *Manager) waitSSH(ctx context.Context, sb *Sandbox, d time.Duration) error {
	deadline := time.Now().Add(d)
	addr := netip.AddrPortFrom(sb.IP, 22).String()
	for {
		if err := m.Run.Dial(ctx, addr); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandbox %s: sshd not reachable at %s (see %s)", sb.ID, addr,
				filepath.Join(m.Cfg.SandboxDir(sb.ID), "serial.log"))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.Wait):
		}
	}
}

// provisionScript mirrors the macOS backend's first-boot setup.
const provisionScript = `set -e
[ -n "$1" ] && git config --global user.name "$1"
[ -n "$2" ] && git config --global user.email "$2"
git config --global init.defaultBranch main
git config --global --add safe.directory /work
git config --global credential.https://github.com.helper \
  '!f() { test "$1" = get && test -n "$GH_TOKEN" && printf "username=x-access-token\npassword=%s\n" "$GH_TOKEN"; }; f'
cat > ~/.claude.json <<'JSON'
{"hasCompletedOnboarding": true, "bypassPermissionsModeAccepted": true,
 "projects": {"/work": {"hasTrustDialogAccepted": true}}}
JSON
if [ -n "$3" ]; then git clone "$3" /work; fi
`

func (m *Manager) provision(ctx context.Context, sb *Sandbox, o CreateOptions) error {
	if err := m.waitSSH(ctx, sb, 60*time.Second); err != nil {
		return err
	}
	m.logf("provisioning %s", sb.ID)
	cmd := fmt.Sprintf("bash -lc %s provision %s %s %s", shellQuote(provisionScript),
		shellQuote(o.GitName), shellQuote(o.GitEmail), shellQuote(o.Repo))
	_, err := m.ssh(ctx, sb, nil, cmd)
	return err
}

// Stop shuts the guest down cleanly (running flavor down hooks), then
// tears down the VMM, virtiofs daemons and tap device.
func (m *Manager) Stop(ctx context.Context, sb *Sandbox, timeout time.Duration) error {
	pid := m.readPid(sb.ID, "ch")
	if m.Run.Alive(pid) {
		// Only a booted guest can shut itself down. sbx-boot (PID 1) traps
		// TERM: it stops sbx-init, which runs each flavor's down hook, then
		// powers the VM off.
		if m.Status(ctx, sb).State == ch.StateRunning {
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			m.ssh(sctx, sb, nil, "sudo kill -TERM 1")
			cancel()
			deadline := time.Now().Add(timeout)
			for m.Run.Alive(pid) && time.Now().Before(deadline) {
				if m.Status(ctx, sb).State != ch.StateRunning {
					break
				}
				time.Sleep(m.Wait)
			}
		}
		c := m.client(sb.ID)
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		c.Shutdown(cctx) // no-op if the guest already powered off
		c.Delete(cctx)
		c.ShutdownVMM(cctx)
		cancel()
	}
	m.cleanupProcesses(sb)
	m.ip(ctx, "link", "del", sb.Tap)
	return nil
}

// cleanupProcesses terminates any VMM or virtiofsd left for sb.
func (m *Manager) cleanupProcesses(sb *Sandbox) {
	for _, name := range []string{"ch", "virtiofsd-pool", "virtiofsd-seed"} {
		pid := m.readPid(sb.ID, name)
		if m.Run.Alive(pid) {
			m.Run.Signal(pid, syscall.SIGTERM)
			for i := 0; i < 20 && m.Run.Alive(pid); i++ {
				time.Sleep(m.Wait)
			}
			if m.Run.Alive(pid) {
				m.Run.Signal(pid, syscall.SIGKILL)
			}
		}
		os.Remove(m.pidFile(sb.ID, name))
	}
}

// SaveHistory archives the agent's Claude Code history to the host.
func (m *Manager) SaveHistory(ctx context.Context, sb *Sandbox) (string, error) {
	out, err := m.ssh(ctx, sb, nil,
		"test -d ~/.claude && tar -C ~ -czf - .claude .claude.json .bash_history 2>/dev/null || test -d ~/.claude")
	if err != nil || len(out) == 0 {
		return "", err
	}
	if err := os.MkdirAll(m.Cfg.HistoryDir(), 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(m.Cfg.HistoryDir(), fmt.Sprintf("%s-%s.tgz", sb.ID, time.Now().Format("20060102-150405")))
	return path, os.WriteFile(path, out, 0o600)
}

// Remove stops sb if needed, optionally saving its history, and deletes it.
func (m *Manager) Remove(ctx context.Context, sb *Sandbox, backup bool) error {
	if backup && m.Status(ctx, sb).Running {
		if p, err := m.SaveHistory(ctx, sb); err == nil && p != "" {
			m.logf("saved agent history to %s", p)
		}
	}
	if err := m.Stop(ctx, sb, 20*time.Second); err != nil {
		return err
	}
	os.RemoveAll(filepath.Join(m.Cfg.RunDir, sb.ID))
	return m.Store.Delete(sb.ID)
}

// Resize changes a running sandbox's vCPUs or reclaims memory with the
// balloon. reclaim is the amount of guest memory to hand back to the host.
func (m *Manager) Resize(ctx context.Context, sb *Sandbox, cpus int, reclaim int64) error {
	if !m.Status(ctx, sb).Running {
		return fmt.Errorf("sandbox %s is not running", sb.ID)
	}
	var r ch.VmResize
	if cpus > 0 {
		if cpus > sb.MaxCPUs {
			return fmt.Errorf("sandbox %s can use at most %d vCPUs", sb.ID, sb.MaxCPUs)
		}
		r.DesiredVcpus = &cpus
	}
	if reclaim >= 0 {
		if reclaim >= sb.Memory {
			return errors.New("cannot reclaim all of the guest's memory")
		}
		r.DesiredBalloon = &reclaim
	}
	if err := m.client(sb.ID).Resize(ctx, r); err != nil {
		return err
	}
	if cpus > 0 {
		sb.CPUs = cpus
		return m.Store.Save(sb)
	}
	return nil
}
