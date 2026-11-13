package ch

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The upstream OpenAPI description of the cloud-hypervisor API. These tests
// make sure every request sbx can send uses real endpoints and real fields.
const specFile = "testdata/cloud-hypervisor-v54.1.yaml"

type spec struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]schema `yaml:"schemas"`
	} `yaml:"components"`
}

type schema struct {
	Ref        string            `yaml:"$ref"`
	Type       string            `yaml:"type"`
	Required   []string          `yaml:"required"`
	Properties map[string]schema `yaml:"properties"`
	Items      *schema           `yaml:"items"`
	Enum       []string          `yaml:"enum"`
}

func loadSpec(t *testing.T) spec {
	t.Helper()
	b, err := os.ReadFile(specFile)
	if err != nil {
		t.Fatal(err)
	}
	var s spec
	if err := yaml.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s spec) resolve(sc schema) schema {
	for sc.Ref != "" {
		sc = s.Components.Schemas[strings.TrimPrefix(sc.Ref, "#/components/schemas/")]
	}
	return sc
}

// check walks a JSON value against a schema, reporting unknown fields,
// missing required fields, and invalid enum values.
func (s spec) check(t *testing.T, where string, v any, sc schema) {
	t.Helper()
	sc = s.resolve(sc)
	switch val := v.(type) {
	case map[string]any:
		for _, req := range sc.Required {
			if _, ok := val[req]; !ok {
				t.Errorf("%s: missing required field %q", where, req)
			}
		}
		for k, child := range val {
			prop, ok := sc.Properties[k]
			if !ok {
				t.Errorf("%s: field %q is not in the API spec", where, k)
				continue
			}
			s.check(t, where+"."+k, child, prop)
		}
	case []any:
		if sc.Items == nil {
			t.Errorf("%s: spec does not describe an array", where)
			return
		}
		for _, item := range val {
			s.check(t, where+"[]", item, *sc.Items)
		}
	case string:
		if len(sc.Enum) > 0 {
			for _, e := range sc.Enum {
				if e == val {
					return
				}
			}
			t.Errorf("%s: %q is not one of %v", where, val, sc.Enum)
		}
	}
}

func roundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func fullConfig() VmConfig {
	return VmConfig{
		Cpus:    &CpusConfig{BootVcpus: 2, MaxVcpus: 8},
		Memory:  &MemoryConfig{Size: 4 << 30, Shared: true},
		Payload: PayloadConfig{Kernel: "/var/lib/sbx/vmlinux", Cmdline: "console=ttyS0 root=/dev/vda rw"},
		Disks:   []DiskConfig{{Path: "/var/lib/sbx/sandboxes/sb1/rootfs.ext4", ID: "root"}},
		Net:     []NetConfig{{Tap: "sbxt-sb1", Mac: "02:73:62:00:00:05", NumQueues: 2, ID: "eth0"}},
		Rng:     &RngConfig{Src: "/dev/urandom"},
		Balloon: &BalloonConfig{Size: 0, DeflateOnOOM: true, FreePageReporting: true},
		Fs: []FsConfig{
			{Tag: "pool", Socket: "/run/sbx/sb1/pool.sock", NumQueues: 1, QueueSize: 1024, ID: "pool"},
			{Tag: "seed", Socket: "/run/sbx/sb1/seed.sock", NumQueues: 1, QueueSize: 1024, ID: "seed"},
		},
		Serial:  &ConsoleConfig{Mode: ConsoleFile, File: "/var/lib/sbx/sandboxes/sb1/serial.log"},
		Console: &ConsoleConfig{Mode: ConsoleOff},
	}
}

func TestVmConfigMatchesSpec(t *testing.T) {
	s := loadSpec(t)
	s.check(t, "VmConfig", roundTrip(t, fullConfig()), s.Components.Schemas["VmConfig"])
}

func TestVmResizeMatchesSpec(t *testing.T) {
	s := loadSpec(t)
	n, ram, bal := 4, int64(8<<30), int64(1<<30)
	s.check(t, "VmResize", roundTrip(t, VmResize{DesiredVcpus: &n, DesiredRAM: &ram, DesiredBalloon: &bal}),
		s.Components.Schemas["VmResize"])
}

func TestResponseTypesMatchSpec(t *testing.T) {
	s := loadSpec(t)
	for name, typ := range map[string]reflect.Type{
		"VmInfo":          reflect.TypeOf(VmInfo{}),
		"VmmPingResponse": reflect.TypeOf(VmmPingResponse{}),
	} {
		sc := s.Components.Schemas[name]
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if _, ok := sc.Properties[tag]; !ok {
				t.Errorf("%s.%s: field %q is not in the API spec", name, typ.Field(i).Name, tag)
			}
		}
	}
	states := s.resolve(s.Components.Schemas["VmState"]).Enum
	for _, st := range []VmState{StateCreated, StateRunning, StateShutdown, StatePaused} {
		if !contains(states, string(st)) {
			t.Errorf("VmState %q not in spec %v", st, states)
		}
	}
}

func TestEndpointsExist(t *testing.T) {
	s := loadSpec(t)
	used := map[string]string{
		"/vmm.ping": "get", "/vmm.shutdown": "put",
		"/vm.create": "put", "/vm.boot": "put", "/vm.shutdown": "put", "/vm.delete": "put",
		"/vm.power-button": "put", "/vm.info": "get", "/vm.resize": "put",
	}
	for path, method := range used {
		ops, ok := s.Paths[path]
		if !ok {
			t.Errorf("endpoint %s not in spec", path)
			continue
		}
		if _, ok := ops[method]; !ok {
			t.Errorf("endpoint %s does not accept %s", path, strings.ToUpper(method))
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
