// Package host runs sbx sandboxes as Cloud Hypervisor microVMs on a Linux
// host. It mirrors the macOS backend: one VM per sandbox, the shared flavor
// pool mounted read-only over virtiofs, and sbx-init as the guest agent.
package host

import (
	"os"
	"path/filepath"
	"strconv"
)

// Config holds host paths and defaults. Every field can be overridden with
// an SBX_HOST_* environment variable (see FromEnv).
type Config struct {
	Root       string   // state, pool, images: /var/lib/sbx
	RunDir     string   // sockets and pid files: /run/sbx
	FlavorDirs []string // directories containing flavor definitions
	Kernel     string   // guest kernel (vmlinux / Image)
	BaseImage  string   // ext4 root filesystem every sandbox is cloned from

	CloudHypervisor string
	Virtiofsd       string

	Bridge string // host bridge the tap devices join
	Subnet string // sandbox addresses; the first host address is the bridge
	DNS    string // resolver handed to guests

	CPUs    int
	MaxCPUs int   // upper bound for live vCPU resize
	Memory  int64 // bytes
	Port    int   // default web port inside the sandbox
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
}

// FromEnv returns the default configuration with environment overrides.
func FromEnv() Config {
	root := env("SBX_HOST_ROOT", "/var/lib/sbx")
	repo := env("SBX_REPO", defaultRepo())
	mem, err := ParseSize(env("SBX_HOST_MEMORY", "4G"))
	if err != nil {
		mem = 4 << 30
	}
	cpus := envInt("SBX_HOST_CPUS", 4)
	return Config{
		Root:            root,
		RunDir:          env("SBX_HOST_RUN", "/run/sbx"),
		FlavorDirs:      filepath.SplitList(env("SBX_FLAVOR_DIRS", filepath.Join(repo, "flavors"))),
		Kernel:          env("SBX_HOST_KERNEL", filepath.Join(root, "images", "vmlinux")),
		BaseImage:       env("SBX_HOST_IMAGE", filepath.Join(root, "images", "rootfs.ext4")),
		CloudHypervisor: env("SBX_HOST_CH", "cloud-hypervisor"),
		Virtiofsd:       env("SBX_HOST_VIRTIOFSD", "virtiofsd"),
		Bridge:          env("SBX_HOST_BRIDGE", "sbxbr0"),
		Subnet:          env("SBX_HOST_SUBNET", "10.77.0.0/24"),
		DNS:             env("SBX_HOST_DNS", "1.1.1.1"),
		CPUs:            cpus,
		MaxCPUs:         envInt("SBX_HOST_MAX_CPUS", cpus*2),
		Memory:          mem,
		Port:            envInt("SBX_HOST_PORT", 3000),
	}
}

// defaultRepo guesses the checkout that contains this binary (bin/sbx-host).
func defaultRepo() string {
	exe, err := os.Executable()
	if err != nil {
		return "/opt/sbx"
	}
	if exe, err = filepath.EvalSymlinks(exe); err == nil {
		return filepath.Dir(filepath.Dir(exe))
	}
	return "/opt/sbx"
}

func (c Config) PoolDir() string             { return filepath.Join(c.Root, "pool") }
func (c Config) SandboxesDir() string        { return filepath.Join(c.Root, "sandboxes") }
func (c Config) SandboxDir(id string) string { return filepath.Join(c.SandboxesDir(), id) }
func (c Config) HistoryDir() string          { return filepath.Join(c.Root, "history") }
func (c Config) SSHKey() string              { return filepath.Join(c.Root, "ssh", "id_ed25519") }
func (c Config) SecretsFile() string         { return filepath.Join(c.Root, "secrets.env") }
func (c Config) RunPath(id, name string) string {
	return filepath.Join(c.RunDir, id, name)
}
