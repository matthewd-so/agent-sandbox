package ch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client talks to one cloud-hypervisor process through its API socket.
type Client struct {
	socket string
	http   *http.Client
}

// APIError is returned when the VMM answers with a non-2xx status.
type APIError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Body)
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("cloud-hypervisor %s %s: %d: %s", e.Method, e.Path, e.Status, msg)
}

// NewClient returns a client for the API socket at path.
func NewClient(socket string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}
	return &Client{socket: socket, http: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost/api/v1/"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Method: method, Path: path, Status: resp.StatusCode, Body: string(data)}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Ping reports the VMM version; it succeeds as soon as the API is up.
func (c *Client) Ping(ctx context.Context) (VmmPingResponse, error) {
	var r VmmPingResponse
	err := c.do(ctx, http.MethodGet, "vmm.ping", nil, &r)
	return r, err
}

// WaitReady polls Ping until it succeeds or ctx expires.
func (c *Client) WaitReady(ctx context.Context) error {
	for {
		if _, err := c.Ping(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cloud-hypervisor API at %s not ready: %w", c.socket, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (c *Client) Create(ctx context.Context, cfg VmConfig) error {
	return c.do(ctx, http.MethodPut, "vm.create", cfg, nil)
}

func (c *Client) Boot(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vm.boot", nil, nil)
}

// Shutdown stops the guest immediately (no guest cooperation).
func (c *Client) Shutdown(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vm.shutdown", nil, nil)
}

// PowerButton sends an ACPI power-button event to the guest.
func (c *Client) PowerButton(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vm.power-button", nil, nil)
}

func (c *Client) Delete(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vm.delete", nil, nil)
}

// ShutdownVMM asks the cloud-hypervisor process itself to exit.
func (c *Client) ShutdownVMM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vmm.shutdown", nil, nil)
}

func (c *Client) Info(ctx context.Context) (VmInfo, error) {
	var info VmInfo
	err := c.do(ctx, http.MethodGet, "vm.info", nil, &info)
	return info, err
}

func (c *Client) Resize(ctx context.Context, r VmResize) error {
	if r.DesiredVcpus == nil && r.DesiredRAM == nil && r.DesiredBalloon == nil {
		return errors.New("resize: nothing to change")
	}
	return c.do(ctx, http.MethodPut, "vm.resize", r, nil)
}
