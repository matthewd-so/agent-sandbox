package host

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Runner abstracts process execution so the manager can be tested without
// root, KVM, or any of the real binaries.
type Runner interface {
	// Run executes a command to completion and returns its combined output.
	Run(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error)
	// Start launches a long-lived daemon in its own session, logging to
	// logPath, and returns its pid.
	Start(name string, args []string, logPath string) (int, error)
	// Alive reports whether pid is still running.
	Alive(pid int) bool
	// Signal sends sig to pid.
	Signal(pid int, sig syscall.Signal) error
	// Dial tests TCP reachability (used to wait for the guest's sshd).
	Dial(ctx context.Context, addr string) error
}

// ExecRunner is the real Runner.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err,
			strings.TrimSpace(out.String()))
	}
	return out.Bytes(), nil
}

func (ExecRunner) Start(name string, args []string, logPath string) (int, error) {
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", name, err)
	}
	pid := cmd.Process.Pid
	go cmd.Wait() // reap; the daemon outlives this call
	return pid, nil
}

func (ExecRunner) Alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func (ExecRunner) Signal(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

func (ExecRunner) Dial(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}
