package ch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeVMM is a tiny in-memory stand-in for the cloud-hypervisor API.
type fakeVMM struct {
	mu      sync.Mutex
	calls   []string
	state   VmState
	created *VmConfig
	resize  *VmResize
}

func (f *fakeVMM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	f.calls = append(f.calls, r.Method+" "+p)
	body, _ := io.ReadAll(r.Body)
	switch r.Method + " " + p {
	case "GET /api/v1/vmm.ping":
		json.NewEncoder(w).Encode(VmmPingResponse{Version: "v54.1"})
	case "PUT /api/v1/vm.create":
		if f.created != nil {
			http.Error(w, "VM already created", http.StatusConflict)
			return
		}
		var cfg VmConfig
		if err := json.Unmarshal(body, &cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.created, f.state = &cfg, StateCreated
		w.WriteHeader(http.StatusNoContent)
	case "PUT /api/v1/vm.boot":
		if f.created == nil {
			http.Error(w, "no VM", http.StatusNotFound)
			return
		}
		f.state = StateRunning
		w.WriteHeader(http.StatusNoContent)
	case "GET /api/v1/vm.info":
		json.NewEncoder(w).Encode(VmInfo{Config: *f.created, State: f.state, MemoryActualSize: f.created.Memory.Size})
	case "PUT /api/v1/vm.resize":
		var rz VmResize
		json.Unmarshal(body, &rz)
		f.resize = &rz
		w.WriteHeader(http.StatusNoContent)
	case "PUT /api/v1/vm.shutdown":
		f.state = StateShutdown
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	// Unix socket paths are limited to ~104 bytes, and t.TempDir() can be
	// longer than that on macOS.
	dir, err := os.MkdirTemp("/tmp", "ch")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestLifecycle(t *testing.T) {
	f := &fakeVMM{}
	c := NewClient(serve(t, f))
	ctx := context.Background()

	if err := c.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := VmConfig{
		Cpus:    &CpusConfig{BootVcpus: 2, MaxVcpus: 4},
		Memory:  &MemoryConfig{Size: 1 << 30, Shared: true},
		Payload: PayloadConfig{Kernel: "/k", Cmdline: "console=ttyS0"},
	}
	if err := c.Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := c.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != StateRunning || info.Config.Cpus.MaxVcpus != 4 {
		t.Fatalf("unexpected info: %+v", info)
	}
	b := int64(512 << 20)
	if err := c.Resize(ctx, VmResize{DesiredBalloon: &b}); err != nil {
		t.Fatal(err)
	}
	if f.resize == nil || *f.resize.DesiredBalloon != b || f.resize.DesiredVcpus != nil {
		t.Fatalf("resize body: %+v", f.resize)
	}
}

func TestAPIError(t *testing.T) {
	f := &fakeVMM{}
	c := NewClient(serve(t, f))
	err := c.Boot(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || apiErr.Path != "vm.boot" {
		t.Fatalf("want 404 APIError, got %v", err)
	}
}

func TestResizeRequiresAField(t *testing.T) {
	if err := NewClient("/nonexistent").Resize(context.Background(), VmResize{}); err == nil {
		t.Fatal("empty resize should fail without calling the API")
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.WaitReady(ctx); err == nil {
		t.Fatal("expected timeout")
	}
}
