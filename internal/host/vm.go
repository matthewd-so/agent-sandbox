package host

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/matthewd-so/agent-sandbox/internal/ch"
)

// Cmdline builds the guest kernel command line. Unrecognised key=value
// parameters are passed by the kernel to init as environment variables,
// which is how image/ch/sbx-boot learns its identity and network settings.
func Cmdline(sb *Sandbox, prefixBits int, gateway, dns, arch string) string {
	console := "ttyS0"
	if arch == "arm64" {
		console = "ttyAMA0"
	}
	return strings.Join([]string{
		"console=" + console,
		"root=/dev/vda", "rw",
		"init=/usr/local/sbin/sbx-boot",
		"quiet", "panic=1",
		"SBX_ID=" + sb.ID,
		// The kernel splits on spaces, so the flavor list travels comma-separated.
		"SBX_FLAVORS=" + strings.Join(sb.Flavors, ","),
		fmt.Sprintf("SBX_IP=%s/%d", sb.IP, prefixBits),
		"SBX_GW=" + gateway,
		"SBX_DNS=" + dns,
	}, " ")
}

// VMConfig returns the cloud-hypervisor configuration for a sandbox.
func (m *Manager) VMConfig(sb *Sandbox) ch.VmConfig {
	dir := m.Cfg.SandboxDir(sb.ID)
	maxCPUs := sb.MaxCPUs
	if maxCPUs < sb.CPUs {
		maxCPUs = sb.CPUs
	}
	return ch.VmConfig{
		Cpus: &ch.CpusConfig{BootVcpus: sb.CPUs, MaxVcpus: maxCPUs},
		// virtiofs is a vhost-user device, which requires shared guest memory.
		Memory: &ch.MemoryConfig{Size: sb.Memory, Shared: true},
		Payload: ch.PayloadConfig{
			Kernel:  m.Cfg.Kernel,
			Cmdline: Cmdline(sb, m.Pool.Prefix.Bits(), m.Pool.Gateway.String(), m.Cfg.DNS, m.arch()),
		},
		Disks: []ch.DiskConfig{{Path: filepath.Join(dir, "rootfs.ext4"), ID: "root"}},
		Net:   []ch.NetConfig{{Tap: sb.Tap, Mac: sb.MAC, NumQueues: 2, ID: "eth0"}},
		Rng:   &ch.RngConfig{Src: "/dev/urandom"},
		// Free page reporting hands memory the guest frees back to the host,
		// and the balloon lets `sbx-host resize --reclaim` take more on demand.
		Balloon: &ch.BalloonConfig{Size: 0, DeflateOnOOM: true, FreePageReporting: true},
		Fs: []ch.FsConfig{
			{Tag: "pool", Socket: m.Cfg.RunPath(sb.ID, "pool.sock"), NumQueues: 1, QueueSize: 1024, ID: "pool"},
			{Tag: "seed", Socket: m.Cfg.RunPath(sb.ID, "seed.sock"), NumQueues: 1, QueueSize: 1024, ID: "seed"},
		},
		Serial:  &ch.ConsoleConfig{Mode: ch.ConsoleFile, File: filepath.Join(dir, "serial.log")},
		Console: &ch.ConsoleConfig{Mode: ch.ConsoleOff},
	}
}

func (m *Manager) arch() string {
	if m.Arch != "" {
		return m.Arch
	}
	return runtime.GOARCH
}
