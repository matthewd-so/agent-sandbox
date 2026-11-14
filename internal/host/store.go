package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// Sandbox is the persisted description of one sandbox. Runtime state
// (running or not) is never stored; it is derived from the VMM process.
type Sandbox struct {
	ID      string     `json:"id"`
	Flavor  string     `json:"flavor"`
	Flavors []string   `json:"flavors"` // closure, dependencies first
	IP      netip.Addr `json:"ip"`
	MAC     string     `json:"mac"`
	Tap     string     `json:"tap"`
	CPUs    int        `json:"cpus"`
	MaxCPUs int        `json:"max_cpus"`
	Memory  int64      `json:"memory"`
	Port    int        `json:"port"`
	Repo    string     `json:"repo,omitempty"`
	Created time.Time  `json:"created"`
}

// ErrNotFound is returned for an unknown sandbox id.
var ErrNotFound = errors.New("no such sandbox")

// Store persists sandboxes as <root>/sandboxes/<id>/sandbox.json.
type Store struct{ cfg Config }

func (s Store) file(id string) string { return filepath.Join(s.cfg.SandboxDir(id), "sandbox.json") }

func (s Store) Save(sb *Sandbox) error {
	if err := os.MkdirAll(s.cfg.SandboxDir(sb.ID), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sb, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file(sb.ID) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file(sb.ID))
}

func (s Store) Load(id string) (*Sandbox, error) {
	b, err := os.ReadFile(s.file(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	var sb Sandbox
	if err := json.Unmarshal(b, &sb); err != nil {
		return nil, fmt.Errorf("sandbox %s: %w", id, err)
	}
	return &sb, nil
}

func (s Store) List() ([]*Sandbox, error) {
	entries, err := os.ReadDir(s.cfg.SandboxesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Sandbox
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sb, err := s.Load(e.Name())
		if err != nil {
			continue // half-created; ignored until removed
		}
		out = append(out, sb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

func (s Store) Delete(id string) error {
	return os.RemoveAll(s.cfg.SandboxDir(id))
}

// lock takes an exclusive host-wide lock (for id and address allocation).
func (s Store) lock() (func(), error) {
	if err := os.MkdirAll(s.cfg.Root, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.cfg.Root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
