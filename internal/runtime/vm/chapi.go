package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type chAPI struct {
	client *http.Client
}

// chAPITimeout backstops a monitor that stops answering; a snapshot of a
// large guest takes seconds, not minutes.
const chAPITimeout = 2 * time.Minute

func newCHAPI(socket string) chAPI {
	return chAPI{client: &http.Client{Timeout: chAPITimeout, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}}
}

// state is vm.info's state: Created, Running, Paused or Shutdown.
func (c chAPI) state(ctx context.Context) (string, error) {
	var info struct {
		State string `json:"state"`
	}
	if err := c.call(ctx, http.MethodGet, "vm.info", nil, &info); err != nil {
		return "", err
	}
	return info.State, nil
}

func (c chAPI) pause(ctx context.Context) error {
	return c.call(ctx, http.MethodPut, "vm.pause", nil, nil)
}

func (c chAPI) resume(ctx context.Context) error {
	return c.call(ctx, http.MethodPut, "vm.resume", nil, nil)
}

func (c chAPI) snapshot(ctx context.Context, dir string) error {
	return c.call(ctx, http.MethodPut, "vm.snapshot", map[string]string{"destination_url": "file://" + dir}, nil)
}

// shutdownVMM ends the monitor process; the guest gets no warning.
func (c chAPI) shutdownVMM(ctx context.Context) error {
	return c.call(ctx, http.MethodPut, "vmm.shutdown", nil, nil)
}

// counters maps each device ID to its counters, such as a disk's read_bytes
// or a net device's rx_bytes.
func (c chAPI) counters(ctx context.Context) (map[string]map[string]uint64, error) {
	var out map[string]map[string]uint64
	return out, c.call(ctx, http.MethodGet, "vm.counters", nil, &out)
}

func (c chAPI) call(ctx context.Context, method, endpoint string, body, out any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("cloud-hypervisor %s: %w", endpoint, err)
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost/api/v1/"+endpoint, payload)
	if err != nil {
		return fmt.Errorf("cloud-hypervisor %s: %w", endpoint, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("cloud-hypervisor %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("cloud-hypervisor %s: %w", endpoint, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("cloud-hypervisor %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("cloud-hypervisor %s: decode reply: %w", endpoint, err)
	}
	return nil
}
