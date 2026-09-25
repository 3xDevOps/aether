package browser

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	containerruntime "github.com/3xDevOps/Aether/internal/runtime"
)

var ErrUnavailable = errors.New("browser companion unavailable; explicit restart required")

type Config struct {
	Image string
	CPULimit float64
	MemoryLimitBytes int64
}

type Run struct {
	ID string
	ContainerID containerruntime.ID
}

type Status struct {
	RunID string `json:"run_id"`
	RunContainer containerruntime.ID `json:"run_container"`
	ContainerID containerruntime.ID `json:"container_id"`
	CreationKey string `json:"creation_key"`
	SessionID string `json:"session_id"`
	State string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type managedRun struct {
	mu sync.Mutex
	client *Client
}

// Manager journals creation intent before asking the runtime to create a
// companion. It never interprets a dead socket as permission to restart a
// browser with lost cookies/pages. The caller admits Config's CPU/memory plus
// the runtime's dedicated shared memory before Ensure/Restart/Resume.
type Manager struct {
	root string
	runtime containerruntime.Runtime
	browser containerruntime.BrowserRuntime
	config Config
	mu sync.Mutex
	runs map[string]*managedRun
}

func NewManager(root string, runtime containerruntime.Runtime, config Config) (*Manager, error) {
	browserRuntime, ok := runtime.(containerruntime.BrowserRuntime)
	if !ok { return nil, errors.New("runtime does not support browser companions") }
	if !filepath.IsAbs(root) { return nil, errors.New("browser resource root must be absolute") }
	if config.Image == "" { return nil, errors.New("browser companion image is required") }
	if config.CPULimit == 0 { config.CPULimit = 1 }
	if config.MemoryLimitBytes == 0 { config.MemoryLimitBytes = 1 << 30 }
	if config.CPULimit < 0 || config.MemoryLimitBytes < 0 { return nil, errors.New("browser resource limits must be positive") }
	if err := os.MkdirAll(root, 0o700); err != nil { return nil, err }
	if err := os.Chmod(root, 0o700); err != nil { return nil, err }
	return &Manager{root: root, runtime: runtime, browser: browserRuntime, config: config, runs: make(map[string]*managedRun)}, nil
}

func (m *Manager) entry(run Run) (*managedRun, string, error) {
	if run.ID == "" || run.ContainerID == "" { return nil, "", errors.New("browser run and container identities are required") }
	digest := sha256.Sum256([]byte(run.ID))
	name := hex.EncodeToString(digest[:16])
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.runs[run.ID]
	if entry == nil { entry = &managedRun{}; m.runs[run.ID] = entry }
	return entry, filepath.Join(m.root, name), nil
}

func loadStatus(dir string) (Status, error) {
	var status Status
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil { return status, err }
	if len(data) > 16384 { return status, errors.New("browser lifecycle journal exceeds limit") }
	if err := json.Unmarshal(data, &status); err != nil { return status, fmt.Errorf("read browser lifecycle journal: %w", err) }
	return status, nil
}

func saveStatus(dir string, status Status) error {
	data, err := json.Marshal(status)
	if err != nil { return err }
	file, err := os.CreateTemp(dir, ".state-")
	if err != nil { return err }
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil { err = file.Sync() }
	closeErr := file.Close()
	if err != nil { return err }
	if closeErr != nil { return closeErr }
	if err := os.Rename(name, filepath.Join(dir, "state.json")); err != nil { return err }
	directory, err := os.Open(dir)
	if err != nil { return err }
	defer directory.Close()
	return directory.Sync()
}

func (m *Manager) reconcile(ctx context.Context, run Run, entry *managedRun, dir string) (Status, *Client, error) {
	status, err := loadStatus(dir)
	if err != nil { return status, nil, err }
	if status.RunID != run.ID || status.RunContainer != run.ContainerID {
		return status, nil, fmt.Errorf("%w: run container identity changed", ErrUnavailable)
	}
	if status.CreationKey == "" { return status, nil, errors.New("browser lifecycle journal has no creation identity") }
	if status.ContainerID == "" {
		status.ContainerID, err = m.runtime.FindByCreationKey(ctx, status.CreationKey)
		if err != nil { return status, nil, fmt.Errorf("%w: find recorded creation: %v", ErrUnavailable, err) }
	}
	info, err := m.browser.InspectBrowser(ctx, status.ContainerID)
	if err != nil { return status, nil, fmt.Errorf("%w: inspect recorded companion: %v", ErrUnavailable, err) }
	if info.CreationKey != status.CreationKey || info.RunContainer != run.ContainerID {
		return status, nil, errors.New("browser companion ownership mismatch")
	}
	status.State = info.State
	if info.State != "running" {
		status.Reason = "Recorded companion is " + info.State
		if err := saveStatus(dir, status); err != nil { return status, nil, err }
		return status, nil, fmt.Errorf("%w: %s", ErrUnavailable, status.Reason)
	}
	if entry.client == nil { entry.client = NewClient(filepath.Join(dir, "control", "browser.sock")) }
	health, err := entry.client.Health(ctx)
	if err != nil { status.Reason = err.Error(); return status, nil, fmt.Errorf("%w: %v", ErrUnavailable, err) }
	if health.CreationKey != status.CreationKey { return status, nil, errors.New("browser socket creation identity mismatch") }
	if status.SessionID != "" && status.SessionID != health.SessionID {
		status.State = "session_lost"
		status.Reason = "Companion process restarted and its previous browser session was lost"
		return status, nil, fmt.Errorf("%w: %s", ErrUnavailable, status.Reason)
	}
	status.SessionID = health.SessionID
	status.Reason = ""
	if err := saveStatus(dir, status); err != nil { return status, nil, err }
	return status, entry.client, nil
}

func (m *Manager) Reconcile(ctx context.Context, run Run) (Status, *Client, error) {
	entry, dir, err := m.entry(run)
	if err != nil { return Status{}, nil, err }
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return m.reconcile(ctx, run, entry, dir)
}

func (m *Manager) Ensure(ctx context.Context, run Run) (Status, *Client, error) {
	entry, dir, err := m.entry(run)
	if err != nil { return Status{}, nil, err }
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err == nil { return m.reconcile(ctx, run, entry, dir) } else if !errors.Is(err, os.ErrNotExist) { return Status{}, nil, err }
	return m.create(ctx, run, entry, dir)
}

func (m *Manager) create(ctx context.Context, run Run, entry *managedRun, dir string) (Status, *Client, error) {
	if err := os.MkdirAll(filepath.Join(dir, "control"), 0o700); err != nil { return Status{}, nil, err }
	// The containing run journal directory remains host-owned 0700. Only its
	// child is mounted into the companion; it is never mounted into the run.
	if err := os.Chown(filepath.Join(dir, "control"), 1000, 1000); err != nil { return Status{}, nil, fmt.Errorf("prepare private browser socket ownership: %w", err) }
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil { return Status{}, nil, err }
	status := Status{RunID: run.ID, RunContainer: run.ContainerID, CreationKey: "browser-"+hex.EncodeToString(nonce[:]), State: "creating"}
	if err := saveStatus(dir, status); err != nil { return status, nil, err }
	id, err := m.browser.CreateBrowser(ctx, containerruntime.BrowserSpec{RunContainer: run.ContainerID, Image: m.config.Image, CreationKey: status.CreationKey, Name: status.CreationKey, ControlHostPath: filepath.Join(dir, "control"), CPULimit: m.config.CPULimit, MemoryLimitBytes: m.config.MemoryLimitBytes})
	if err != nil { return status, nil, err }
	status.ContainerID = id
	if err := saveStatus(dir, status); err != nil { return status, nil, err }
	if err := m.runtime.Start(ctx, id); err != nil { return status, nil, err }
	entry.client = NewClient(filepath.Join(dir, "control", "browser.sock"))
	ready, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var lastError error
	for {
		if _, err := entry.client.Health(ready); err == nil { break } else { lastError = err }
		select {
		case <-ready.Done(): return status, nil, fmt.Errorf("browser companion did not become ready: %w: %v", ready.Err(), lastError)
		case <-time.After(100*time.Millisecond):
		}
	}
	return m.reconcile(ctx, run, entry, dir)
}

func (m *Manager) transition(ctx context.Context, run Run, action string) (Status, error) {
	entry, dir, err := m.entry(run)
	if err != nil { return Status{}, err }
	entry.mu.Lock()
	defer entry.mu.Unlock()
	status, err := loadStatus(dir)
	if err != nil { return status, err }
	if status.RunID != run.ID || status.RunContainer != run.ContainerID { return status, errors.New("browser lifecycle run identity mismatch") }
	if status.ContainerID == "" {
		status.ContainerID, err = m.runtime.FindByCreationKey(ctx, status.CreationKey)
		if err != nil && !(errors.Is(err, containerruntime.ErrNotFound) && (action == "remove" || action == "restart")) { return status, err }
	}
	if status.ContainerID != "" {
		info, inspectErr := m.browser.InspectBrowser(ctx, status.ContainerID)
		if inspectErr != nil && !errors.Is(inspectErr, containerruntime.ErrNotFound) { return status, inspectErr }
		if inspectErr == nil && (info.CreationKey != status.CreationKey || info.RunContainer != status.RunContainer) { return status, errors.New("browser lifecycle resource ownership mismatch") }
	}
	switch action {
	case "pause":
		if err := m.runtime.Pause(ctx, status.ContainerID); err != nil { return status, err }
		status.State = "paused"
	case "resume":
		if err := m.runtime.Resume(ctx, status.ContainerID); err != nil { return status, err }
		status.State = "running"
	case "remove", "restart":
		if status.ContainerID != "" { if err := m.runtime.Destroy(ctx, status.ContainerID); err != nil { return status, err } }
		if entry.client != nil { entry.client.Close(); entry.client = nil }
		if err := os.RemoveAll(dir); err != nil { return status, err }
		if action == "restart" { fresh, _, err := m.create(ctx, run, entry, dir); return fresh, err }
		status.State = "removed"
		return status, nil
	}
	if err := saveStatus(dir, status); err != nil { return status, err }
	if action == "resume" { current, _, err := m.reconcile(ctx, run, entry, dir); return current, err }
	return status, nil
}

func (m *Manager) Pause(ctx context.Context, run Run) (Status, error) { return m.transition(ctx, run, "pause") }
func (m *Manager) Resume(ctx context.Context, run Run) (Status, error) { return m.transition(ctx, run, "resume") }
func (m *Manager) Restart(ctx context.Context, run Run) (Status, error) { return m.transition(ctx, run, "restart") }
func (m *Manager) Remove(ctx context.Context, run Run) error {
	_, err := m.transition(ctx, run, "remove")
	if errors.Is(err, os.ErrNotExist) { return nil }
	return err
}
