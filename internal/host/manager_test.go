package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/matthewd-so/agent-sandbox/internal/ch"
)

// fakeHost stands in for the real binaries: cloud-hypervisor becomes an
// in-process HTTP server on its API socket, virtiofsd a listening socket,
// and the guest an ssh endpoint that records the scripts it is sent.
type fakeHost struct {
	mu         sync.Mutex
	nextPid    int
	procs      map[int]io.Closer
	vmms       map[string]*fakeVMM // by api socket
	cmds       []string
	ssh        []string
	failCreate bool
}

type fakeVMM struct {
	mu     sync.Mutex
	config *ch.VmConfig
	state  ch.VmState
	resize *ch.VmResize
}

func (v *fakeVMM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	defer v.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	switch r.Method + " " + strings.TrimPrefix(r.URL.Path, "/api/v1/") {
	case "GET vmm.ping":
		w.Write([]byte(`{"version":"fake"}`))
	case "PUT vm.create":
		var c ch.VmConfig
		if err := json.Unmarshal(body, &c); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		v.config, v.state = &c, ch.StateCreated
		w.WriteHeader(204)
	case "PUT vm.boot":
		v.state = ch.StateRunning
		w.WriteHeader(204)
	case "GET vm.info":
		if v.config == nil {
			http.Error(w, "no vm", 404)
			return
		}
		json.NewEncoder(w).Encode(ch.VmInfo{Config: *v.config, State: v.state, MemoryActualSize: v.config.Memory.Size})
	case "PUT vm.resize":
		var rz ch.VmResize
		json.Unmarshal(body, &rz)
		v.resize = &rz
		w.WriteHeader(204)
	case "PUT vm.shutdown", "PUT vm.delete", "PUT vmm.shutdown", "PUT vm.power-button":
		v.state = ch.StateShutdown
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

type serverCloser struct{ *http.Server }

func (s serverCloser) Close() error { return s.Server.Close() }

func (f *fakeHost) Run(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch name {
	case "cp":
		dst := args[len(args)-1]
		return nil, os.WriteFile(dst, []byte("rootfs"), 0o600)
	case "ssh":
		script := args[len(args)-1]
		f.ssh = append(f.ssh, script)
		if script == "sudo kill -TERM 1" { // guest powers itself off
			for _, v := range f.vmms {
				v.mu.Lock()
				v.state = ch.StateShutdown
				v.mu.Unlock()
			}
		}
		return nil, nil
	}
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	return nil, nil
}

func (f *fakeHost) Start(name string, args []string, logPath string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextPid++
	pid := f.nextPid
	switch filepath.Base(name) {
	case "cloud-hypervisor":
		sock := strings.TrimPrefix(args[1], "path=")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			return 0, err
		}
		v := &fakeVMM{}
		var h http.Handler = v
		if f.failCreate {
			h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "vm.create") {
					http.Error(w, "boom", 500)
					return
				}
				v.ServeHTTP(w, r)
			})
		}
		srv := &http.Server{Handler: h}
		go srv.Serve(ln)
		f.vmms[sock] = v
		f.procs[pid] = serverCloser{srv}
	case "virtiofsd":
		ln, err := net.Listen("unix", args[1]) // --socket-path <sock>
		if err != nil {
			return 0, err
		}
		f.procs[pid] = ln
	default:
		return 0, fmt.Errorf("unexpected daemon %s", name)
	}
	return pid, nil
}

func (f *fakeHost) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.procs[pid]
	return ok
}

func (f *fakeHost) Signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.procs[pid]; ok {
		c.Close()
		delete(f.procs, pid)
	}
	return nil
}

func (f *fakeHost) Dial(ctx context.Context, addr string) error { return nil }

func (f *fakeHost) vmm(t *testing.T, m *Manager, id string) *fakeVMM {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.vmms[m.Cfg.RunPath(id, "api.sock")]
	if v == nil {
		t.Fatalf("no VMM for %s", id)
	}
	return v
}

// setup builds a throwaway host: flavors, a published pool, an SSH key and
// a base image. Unix sockets live under /tmp to stay within path limits.
func setup(t *testing.T) (*Manager, *fakeHost) {
	t.Helper()
	root := t.TempDir()
	run, err := os.MkdirTemp("/tmp", "sbxh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })

	flavors := filepath.Join(root, "flavors")
	write := func(p, s string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(flavors, "node", "flavor"), "DESC=\"Node\"\nDEPS=\"\"\n")
	write(filepath.Join(flavors, "claude", "flavor"), "DESC=\"Claude Code\"\nDEPS=\"node\"\n")
	write(filepath.Join(flavors, "default", "flavor"), "# comment\nDESC=\"All\"\nDEPS=\"claude\"\n")
	write(filepath.Join(flavors, "loop-a", "flavor"), "DEPS=\"loop-b\"\n")
	write(filepath.Join(flavors, "loop-b", "flavor"), "DEPS=\"loop-a\"\n")
	write(filepath.Join(flavors, "broken", "flavor"), "DEPS=\"missing\"\n")

	cfg := Config{
		Root: filepath.Join(root, "var"), RunDir: run, FlavorDirs: []string{flavors},
		Kernel: "/boot/vmlinux", BaseImage: filepath.Join(root, "rootfs.ext4"),
		CloudHypervisor: "cloud-hypervisor", Virtiofsd: "virtiofsd",
		Bridge: "sbxbr0", Subnet: "10.77.0.0/24", DNS: "1.1.1.1",
		CPUs: 2, MaxCPUs: 4, Memory: 2 << 30, Port: 3000,
	}
	for _, f := range []string{"node", "claude", "default"} {
		build := f + "-abc123"
		os.MkdirAll(filepath.Join(cfg.PoolDir(), "store", build), 0o755)
		write(filepath.Join(cfg.PoolDir(), "ptr", f), build+"\n")
	}
	write(cfg.SSHKey()+".pub", "ssh-ed25519 AAAA test\n")
	write(cfg.SecretsFile(), "GH_TOKEN=secret\n")
	write(cfg.BaseImage, "base")

	fh := &fakeHost{procs: map[int]io.Closer{}, vmms: map[string]*fakeVMM{}}
	m, err := NewManager(cfg, fh)
	if err != nil {
		t.Fatal(err)
	}
	m.Arch = "amd64"
	m.Wait = 5 * time.Millisecond
	t.Cleanup(func() {
		for _, c := range fh.procs {
			c.Close()
		}
	})
	return m, fh
}

func TestClosure(t *testing.T) {
	m, _ := setup(t)
	got, err := Closure(m.Cfg.FlavorDirs, "default")
	if err != nil || strings.Join(got, " ") != "node claude default" {
		t.Fatalf("closure = %v, %v", got, err)
	}
	if _, err := Closure(m.Cfg.FlavorDirs, "loop-a"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want cycle error, got %v", err)
	}
	if _, err := Closure(m.Cfg.FlavorDirs, "broken"); err == nil || !strings.Contains(err.Error(), "needed by broken") {
		t.Fatalf("want missing-dependency error, got %v", err)
	}
}

func TestCreateBootsAndProvisions(t *testing.T) {
	m, fh := setup(t)
	ctx := context.Background()
	sb, err := m.Create(ctx, CreateOptions{ID: "sb1", Repo: "https://example.com/r.git", GitName: "Ada", GitEmail: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if sb.IP.String() != "10.77.0.2" || sb.Tap != "sbxt-sb1" || sb.MAC != "02:73:62:4d:00:02" {
		t.Fatalf("allocation: %+v", sb)
	}

	cfg := fh.vmm(t, m, "sb1").config
	if cfg == nil {
		t.Fatal("VM was never created")
	}
	if cfg.Cpus.BootVcpus != 2 || cfg.Cpus.MaxVcpus != 4 || cfg.Memory.Size != 2<<30 || !cfg.Memory.Shared {
		t.Errorf("cpu/memory config: %+v %+v", cfg.Cpus, cfg.Memory)
	}
	if len(cfg.Fs) != 2 || cfg.Fs[0].Tag != "pool" || cfg.Fs[1].Tag != "seed" {
		t.Errorf("virtiofs shares: %+v", cfg.Fs)
	}
	if cfg.Net[0].Tap != "sbxt-sb1" || cfg.Net[0].Mac != sb.MAC {
		t.Errorf("net: %+v", cfg.Net)
	}
	if cfg.Balloon == nil || !cfg.Balloon.FreePageReporting {
		t.Errorf("balloon: %+v", cfg.Balloon)
	}
	for _, want := range []string{"console=ttyS0", "SBX_ID=sb1", "SBX_FLAVORS=node,claude,default", "SBX_IP=10.77.0.2/24", "SBX_GW=10.77.0.1"} {
		if !strings.Contains(cfg.Payload.Cmdline, want) {
			t.Errorf("cmdline %q missing %q", cfg.Payload.Cmdline, want)
		}
	}
	if st := m.Status(ctx, sb); !st.Running || st.State != ch.StateRunning {
		t.Errorf("status: %+v", st)
	}

	cmds := strings.Join(fh.cmds, "\n")
	for _, want := range []string{"ip tuntap add dev sbxt-sb1 mode tap", "master sbxbr0", "isolated on", "ip link set dev sbxt-sb1 up"} {
		if !strings.Contains(cmds, want) {
			t.Errorf("missing host command %q in:\n%s", want, cmds)
		}
	}
	if len(fh.ssh) != 1 || !strings.Contains(fh.ssh[0], "'https://example.com/r.git'") || !strings.Contains(fh.ssh[0], "'Ada'") {
		t.Errorf("provisioning: %q", fh.ssh)
	}

	seed := filepath.Join(m.Cfg.SandboxDir("sb1"), "seed")
	if b, _ := os.ReadFile(filepath.Join(seed, "secrets.env")); string(b) != "GH_TOKEN=secret\n" {
		t.Errorf("seed secrets: %q", b)
	}
	if _, err := os.Stat(filepath.Join(seed, "authorized_keys")); err != nil {
		t.Error(err)
	}
	if got, err := m.Store.Load("sb1"); err != nil || got.IP != sb.IP {
		t.Errorf("persisted: %+v %v", got, err)
	}
}

func TestStopAndRemove(t *testing.T) {
	m, fh := setup(t)
	ctx := context.Background()
	sb, err := m.Create(ctx, CreateOptions{ID: "sb2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx, sb, time.Second); err != nil {
		t.Fatal(err)
	}
	if fh.ssh[len(fh.ssh)-1] != "sudo kill -TERM 1" {
		t.Errorf("expected a graceful guest shutdown, got %q", fh.ssh)
	}
	if m.Status(ctx, sb).Running || len(fh.procs) != 0 {
		t.Errorf("processes still running after stop: %d", len(fh.procs))
	}
	if !strings.Contains(strings.Join(fh.cmds, "\n"), "ip link del sbxt-sb2") {
		t.Error("tap device not removed")
	}

	if err := m.Start(ctx, sb); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !m.Status(ctx, sb).Running {
		t.Fatal("not running after restart")
	}
	if err := m.Remove(ctx, sb, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Store.Load("sb2"); err == nil {
		t.Error("sandbox still on disk after remove")
	}
}

func TestResize(t *testing.T) {
	m, fh := setup(t)
	ctx := context.Background()
	sb, err := m.Create(ctx, CreateOptions{ID: "sb3"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Resize(ctx, sb, 8, -1); err == nil {
		t.Error("resize beyond max vCPUs should fail")
	}
	if err := m.Resize(ctx, sb, 0, sb.Memory); err == nil {
		t.Error("reclaiming all memory should fail")
	}
	if err := m.Resize(ctx, sb, 4, 1<<30); err != nil {
		t.Fatal(err)
	}
	rz := fh.vmm(t, m, "sb3").resize
	if rz == nil || *rz.DesiredVcpus != 4 || *rz.DesiredBalloon != 1<<30 {
		t.Fatalf("resize request: %+v", rz)
	}
	if got, _ := m.Store.Load("sb3"); got.CPUs != 4 {
		t.Errorf("new vCPU count not persisted: %d", got.CPUs)
	}
}

func TestCreateFailureCleansUp(t *testing.T) {
	m, fh := setup(t)
	fh.failCreate = true
	if _, err := m.Create(context.Background(), CreateOptions{ID: "bad"}); err == nil {
		t.Fatal("expected vm.create failure")
	}
	if _, err := os.Stat(m.Cfg.SandboxDir("bad")); !os.IsNotExist(err) {
		t.Error("failed sandbox left on disk")
	}
	if len(fh.procs) != 0 {
		t.Errorf("%d processes leaked", len(fh.procs))
	}
}

func TestCreateRejectsUnbuiltFlavor(t *testing.T) {
	m, _ := setup(t)
	os.Remove(filepath.Join(m.Cfg.PoolDir(), "ptr", "node"))
	_, err := m.Create(context.Background(), CreateOptions{ID: "x"})
	if err == nil || !strings.Contains(err.Error(), "flavor node is not built") {
		t.Fatalf("got %v", err)
	}
}

func TestConcurrentReserveIsUnique(t *testing.T) {
	m, _ := setup(t)
	const n = 30
	var wg sync.WaitGroup
	ips := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sb, err := m.reserve(CreateOptions{CPUs: 1, Memory: 1 << 30}, []string{"node"})
			if err != nil {
				t.Error(err)
				return
			}
			ips <- sb.IP.String() + " " + sb.ID
		}()
	}
	wg.Wait()
	close(ips)
	seenIP, seenID := map[string]bool{}, map[string]bool{}
	for v := range ips {
		ip, id, _ := strings.Cut(v, " ")
		if seenIP[ip] || seenID[id] {
			t.Fatalf("duplicate allocation %s", v)
		}
		seenIP[ip], seenID[id] = true, true
	}
	if len(seenIP) != n {
		t.Fatalf("got %d allocations, want %d", len(seenIP), n)
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]int64{"512M": 512 << 20, "4G": 4 << 30, "4GiB": 4 << 30, "1.5G": 3 << 29, "1024": 1024} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v", in, got, err)
		}
	}
	if _, err := ParseSize("lots"); err == nil {
		t.Error("ParseSize should reject garbage")
	}
	for _, bad := range []string{"", "Upper", "has space", "waytoolongname"} {
		if ValidID(bad) == nil {
			t.Errorf("ValidID(%q) should fail", bad)
		}
	}
	sb := &Sandbox{ID: "a", Flavors: []string{"node"}}
	if !strings.Contains(Cmdline(sb, 24, "10.0.0.1", "1.1.1.1", "arm64"), "console=ttyAMA0") {
		t.Error("arm64 guests use the PL011 console")
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote = %s", got)
	}
}
