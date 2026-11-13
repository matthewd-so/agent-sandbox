// Package ch is a minimal client for the Cloud Hypervisor REST API.
//
// Only the parts of the API that sbx uses are modelled. Field names follow
// the upstream OpenAPI description (testdata/cloud-hypervisor-v54.1.yaml),
// and spec_test.go checks every request body against it.
package ch

// VmConfig is the body of PUT /vm.create.
type VmConfig struct {
	Cpus    *CpusConfig    `json:"cpus,omitempty"`
	Memory  *MemoryConfig  `json:"memory,omitempty"`
	Payload PayloadConfig  `json:"payload"`
	Disks   []DiskConfig   `json:"disks,omitempty"`
	Net     []NetConfig    `json:"net,omitempty"`
	Rng     *RngConfig     `json:"rng,omitempty"`
	Balloon *BalloonConfig `json:"balloon,omitempty"`
	Fs      []FsConfig     `json:"fs,omitempty"`
	Serial  *ConsoleConfig `json:"serial,omitempty"`
	Console *ConsoleConfig `json:"console,omitempty"`
}

type CpusConfig struct {
	BootVcpus int `json:"boot_vcpus"`
	MaxVcpus  int `json:"max_vcpus"`
}

type MemoryConfig struct {
	Size int64 `json:"size"`
	// Shared memory is required for vhost-user devices such as virtiofs.
	Shared bool `json:"shared,omitempty"`
}

type PayloadConfig struct {
	Kernel  string `json:"kernel,omitempty"`
	Cmdline string `json:"cmdline,omitempty"`
}

type DiskConfig struct {
	Path     string `json:"path"`
	Readonly bool   `json:"readonly,omitempty"`
	ID       string `json:"id,omitempty"`
}

type NetConfig struct {
	Tap       string `json:"tap,omitempty"`
	Mac       string `json:"mac,omitempty"`
	NumQueues int    `json:"num_queues,omitempty"`
	ID        string `json:"id,omitempty"`
}

type RngConfig struct {
	Src string `json:"src"`
}

type BalloonConfig struct {
	Size              int64 `json:"size"`
	DeflateOnOOM      bool  `json:"deflate_on_oom,omitempty"`
	FreePageReporting bool  `json:"free_page_reporting,omitempty"`
}

type FsConfig struct {
	Tag       string `json:"tag"`
	Socket    string `json:"socket"`
	NumQueues int    `json:"num_queues"`
	QueueSize int    `json:"queue_size"`
	ID        string `json:"id,omitempty"`
}

// ConsoleMode is one of Off, Pty, Tty, File, Socket, Null.
type ConsoleMode string

const (
	ConsoleOff  ConsoleMode = "Off"
	ConsoleFile ConsoleMode = "File"
	ConsoleNull ConsoleMode = "Null"
)

type ConsoleConfig struct {
	Mode ConsoleMode `json:"mode"`
	File string      `json:"file,omitempty"`
}

// VmResize is the body of PUT /vm.resize. Nil fields are left unchanged.
type VmResize struct {
	DesiredVcpus   *int   `json:"desired_vcpus,omitempty"`
	DesiredRAM     *int64 `json:"desired_ram,omitempty"`
	DesiredBalloon *int64 `json:"desired_balloon,omitempty"`
}

// VmState is one of Created, Running, Shutdown, Paused, BreakPoint.
type VmState string

const (
	StateCreated  VmState = "Created"
	StateRunning  VmState = "Running"
	StateShutdown VmState = "Shutdown"
	StatePaused   VmState = "Paused"
)

// VmInfo is the response of GET /vm.info.
type VmInfo struct {
	Config           VmConfig `json:"config"`
	State            VmState  `json:"state"`
	MemoryActualSize int64    `json:"memory_actual_size,omitempty"`
}

// VmmPingResponse is the response of GET /vmm.ping.
type VmmPingResponse struct {
	Version      string `json:"version"`
	BuildVersion string `json:"build_version,omitempty"`
	Pid          int64  `json:"pid,omitempty"`
}
