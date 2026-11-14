// Command sbx-host runs sbx sandboxes as Cloud Hypervisor microVMs on a
// Linux server. The sbx CLI forwards to it over SSH when SBX_REMOTE is set.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/matthewd-so/agent-sandbox/internal/host"
)

var version = "dev"

const usage = `sbx-host: Cloud Hypervisor backend for sbx

Usage: sbx-host <command> [args]

  init                      check prerequisites; create state dirs and SSH key
  new [flags]               create a sandbox
                              -n name  -f flavor  --repo url  --cpus n
                              --memory 4G  --port 3000  --git-name s  --git-email s
  ls [--json]               list sandboxes
  sh <id>                   shell as the agent user
  exec <id> <cmd...>        run a command
  claude <id> [args...]     run Claude Code with permission prompts off
  start <id...>             boot stopped sandboxes
  stop <id...>              shut sandboxes down cleanly
  rm [--no-backup] <id...>  delete sandboxes (agent history is archived first)
  resize <id> [--cpus n] [--reclaim 1G]
                            change vCPUs or return guest memory via the balloon
  info <id>                 VM details from the hypervisor (JSON)
  logs <id>                 guest serial console and VMM logs
  auth --stdin              replace the credentials given to new sandboxes
  version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func info(format string, a ...any) { fmt.Fprintf(os.Stderr, "==> "+format+"\n", a...) }

func needRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("this command manages tap devices and VMs; run it as root")
	}
	return nil
}

func manager() (*host.Manager, error) {
	m, err := host.NewManager(host.FromEnv(), host.ExecRunner{})
	if err != nil {
		return nil, err
	}
	m.Log = func(s string) { info("%s", s) }
	return m, nil
}

func run(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "version":
		fmt.Println(version)
		return nil
	}
	m, err := manager()
	if err != nil {
		return err
	}
	switch cmd {
	case "init":
		return cmdInit(ctx, m)
	case "new":
		return cmdNew(ctx, m, args)
	case "ls":
		return cmdList(ctx, m, args)
	case "sh", "exec", "claude":
		return cmdSSH(m, cmd, args)
	case "start", "stop", "rm":
		return cmdLifecycle(ctx, m, cmd, args)
	case "resize":
		return cmdResize(ctx, m, args)
	case "info":
		return cmdInfo(ctx, m, args)
	case "logs":
		return cmdLogs(m, args)
	case "auth":
		return cmdAuth(m, args)
	}
	return fmt.Errorf("unknown command %q (see sbx-host help)", cmd)
}

func cmdInit(ctx context.Context, m *host.Manager) error {
	if err := needRoot(); err != nil {
		return err
	}
	c := m.Cfg
	ok := true
	check := func(what string, err error) {
		if err != nil {
			ok = false
			fmt.Printf("  ✗ %-18s %v\n", what, err)
		} else {
			fmt.Printf("  ✓ %s\n", what)
		}
	}
	_, err := os.Stat("/dev/kvm")
	check("/dev/kvm", err)
	for _, bin := range []string{c.CloudHypervisor, c.Virtiofsd, "ip", "ssh", "ssh-keygen"} {
		_, err := exec.LookPath(bin)
		check(filepath.Base(bin), err)
	}
	_, err = os.Stat(c.Kernel)
	check("guest kernel", err)
	_, err = os.Stat(c.BaseImage)
	check("base image", err)
	_, err = m.Run.Run(ctx, nil, "ip", "link", "show", c.Bridge)
	check("bridge "+c.Bridge, err)

	for _, d := range []string{c.PoolDir() + "/store", c.PoolDir() + "/ptr", c.SandboxesDir(), c.HistoryDir(), filepath.Dir(c.SSHKey()), c.RunDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if _, err := os.Stat(c.SSHKey()); errors.Is(err, os.ErrNotExist) {
		if _, err := m.Run.Run(ctx, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "sbx-host", "-f", c.SSHKey()); err != nil {
			return err
		}
		info("generated %s", c.SSHKey())
	}
	if !ok {
		return errors.New("prerequisites missing (scripts/ch-host-setup.sh installs them)")
	}
	info("host ready")
	return nil
}

func cmdNew(ctx context.Context, m *host.Manager, args []string) error {
	if err := needRoot(); err != nil {
		return err
	}
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	var o host.CreateOptions
	var mem string
	fs.StringVar(&o.ID, "n", "", "sandbox name")
	fs.StringVar(&o.Flavor, "f", "default", "flavor")
	fs.StringVar(&o.Repo, "repo", "", "git URL to clone into /work")
	fs.IntVar(&o.CPUs, "cpus", 0, "vCPUs")
	fs.StringVar(&mem, "memory", "", "memory, e.g. 4G")
	fs.IntVar(&o.Port, "port", 0, "default web port")
	fs.StringVar(&o.GitName, "git-name", "", "git user.name in the sandbox")
	fs.StringVar(&o.GitEmail, "git-email", "", "git user.email in the sandbox")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if mem != "" {
		n, err := host.ParseSize(mem)
		if err != nil {
			return err
		}
		o.Memory = n
	}
	start := time.Now()
	sb, err := m.Create(ctx, o)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, `
  sandbox  %s   (%s, ready in %.1fs)
  web      http://%s:%d
  shell    sbx-host sh %s
  agent    sbx-host claude %s
  remove   sbx-host rm %s

`, sb.ID, sb.IP, time.Since(start).Seconds(), sb.IP, sb.Port, sb.ID, sb.ID, sb.ID)
	fmt.Println(sb.ID)
	return nil
}

func cmdList(ctx context.Context, m *host.Manager, args []string) error {
	list, err := m.Store.List()
	if err != nil {
		return err
	}
	type row struct {
		*host.Sandbox
		State  string `json:"state"`
		Actual int64  `json:"memory_actual,omitempty"`
	}
	rows := make([]row, 0, len(list))
	for _, sb := range list {
		st := m.Status(ctx, sb)
		state := "stopped"
		if st.Running {
			state = "running"
		} else if st.State != "" {
			state = strings.ToLower(string(st.State))
		}
		rows = append(rows, row{sb, state, st.MemoryActual})
	}
	if len(args) > 0 && args[0] == "--json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tFLAVOR\tIP\tCPUS\tMEM\tCREATED")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", r.ID, r.State, r.Flavor, r.IP, r.CPUs,
			host.FormatSize(r.Memory), r.Created.Local().Format("2006-01-02 15:04"))
	}
	return w.Flush()
}

func load(m *host.Manager, id string) (*host.Sandbox, error) {
	if id == "" {
		return nil, errors.New("missing sandbox id")
	}
	return m.Store.Load(id)
}

// cmdSSH replaces this process with ssh so the terminal is handed over.
func cmdSSH(m *host.Manager, cmd string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sbx-host %s <id> ...", cmd)
	}
	sb, err := load(m, args[0])
	if err != nil {
		return err
	}
	rest := args[1:]
	var remote string
	switch cmd {
	case "sh":
		remote = "cd /work && exec bash -l"
	case "claude":
		remote = "cd /work && exec bash -lc 'exec claude --dangerously-skip-permissions \"$@\"' claude" + quoteAll(rest)
	case "exec":
		if len(rest) == 0 {
			return errors.New("usage: sbx-host exec <id> <cmd...>")
		}
		remote = "cd /work && exec bash -lc '\"$@\"' bash" + quoteAll(rest)
	}
	tty := cmd != "exec" || isTerminal(os.Stdin)
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}
	argv := append([]string{"ssh"}, m.SSHArgs(sb, tty)...)
	return syscall.Exec(sshPath, append(argv, remote), os.Environ())
}

func quoteAll(args []string) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString(" '" + strings.ReplaceAll(a, "'", `'\''`) + "'")
	}
	return b.String()
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func cmdLifecycle(ctx context.Context, m *host.Manager, cmd string, args []string) error {
	if err := needRoot(); err != nil {
		return err
	}
	backup := true
	var ids []string
	for _, a := range args {
		if a == "--no-backup" && cmd == "rm" {
			backup = false
			continue
		}
		ids = append(ids, a)
	}
	if len(ids) == 0 {
		return fmt.Errorf("usage: sbx-host %s <id...>", cmd)
	}
	for _, id := range ids {
		sb, err := load(m, id)
		if err != nil {
			return err
		}
		switch cmd {
		case "start":
			err = m.Start(ctx, sb)
		case "stop":
			err = m.Stop(ctx, sb, 20*time.Second)
		case "rm":
			err = m.Remove(ctx, sb, backup)
		}
		if err != nil {
			return fmt.Errorf("%s %s: %w", cmd, id, err)
		}
		info("%s %s", map[string]string{"start": "started", "stop": "stopped", "rm": "removed"}[cmd], id)
	}
	return nil
}

func cmdResize(ctx context.Context, m *host.Manager, args []string) error {
	if err := needRoot(); err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: sbx-host resize <id> [--cpus n] [--reclaim 1G]")
	}
	fs := flag.NewFlagSet("resize", flag.ContinueOnError)
	cpus := fs.Int("cpus", 0, "vCPUs")
	reclaim := fs.String("reclaim", "", "guest memory to return to the host (0 to give it all back)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	sb, err := load(m, args[0])
	if err != nil {
		return err
	}
	bal := int64(-1)
	if *reclaim != "" {
		if bal, err = host.ParseSize(*reclaim); err != nil {
			return err
		}
	}
	if *cpus == 0 && bal < 0 {
		return errors.New("nothing to change: pass --cpus and/or --reclaim")
	}
	if err := m.Resize(ctx, sb, *cpus, bal); err != nil {
		return err
	}
	info("resized %s", sb.ID)
	return nil
}

func cmdInfo(ctx context.Context, m *host.Manager, args []string) error {
	sb, err := load(m, firstArg(args))
	if err != nil {
		return err
	}
	out := map[string]any{"sandbox": sb, "status": m.Status(ctx, sb), "vm": m.VMConfig(sb)}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdLogs(m *host.Manager, args []string) error {
	sb, err := load(m, firstArg(args))
	if err != nil {
		return err
	}
	for _, name := range []string{"serial.log", "vmm.log"} {
		p := filepath.Join(m.Cfg.SandboxDir(sb.ID), name)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		if len(lines) > 60 {
			lines = lines[len(lines)-60:]
		}
		fmt.Printf("== %s\n%s\n", p, strings.Join(lines, "\n"))
	}
	return nil
}

func cmdAuth(m *host.Manager, args []string) error {
	if err := needRoot(); err != nil {
		return err
	}
	if firstArg(args) != "--stdin" {
		return errors.New("usage: sbx-host auth --stdin < secrets.env")
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
	if err != nil {
		return err
	}
	p := m.Cfg.SecretsFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		return err
	}
	info("saved credentials for new sandboxes")
	return nil
}

func firstArg(a []string) string {
	if len(a) == 0 {
		return ""
	}
	return a[0]
}
