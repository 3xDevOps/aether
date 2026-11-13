package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

// Files in a VM's directory, <data dir>/vm/<creation key>/.
const (
	recordFile  = "vm.json"
	launchFile  = "launch.json"
	apiSocket   = "api.sock"
	vsockSocket = "vsock.sock"
	fsSocket    = "fs.sock"
	homeSocket  = "home.sock"
	shareDir    = "share"
	helperImage = "helper.img"
	rootDisk    = "root.img"
	dataDisk    = "data.img"
	cacheDisk   = "cache.qcow2"
	consoleLog  = "console.log"
	eventsFile  = "events.json"
	monitorLog  = "monitor.log"
	snapshotDir = "snapshot"
)

// keyPattern keeps a creation key usable as a directory, a systemd unit name
// and a guest hostname.
var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// record is everything a VM needs to boot again and to answer Inspect
// without reaching the guest. Env is the image's environment overlaid with
// the spec's, which is what a container reports.
type record struct {
	Key          string          `json:"key"`
	Fingerprint  string          `json:"fingerprint,omitempty"`
	Image        string          `json:"image"`
	Base         vmimage.Base    `json:"base"`
	Root         vmimage.Disk    `json:"root"`
	Data         vmimage.Disk    `json:"data"`
	Cache        vmimage.Disk    `json:"cache,omitzero"`
	User         string          `json:"user,omitempty"`
	Env          []string        `json:"env"`
	WorkingDir   string          `json:"working_dir,omitempty"`
	Command      []string        `json:"command"`
	TTY          bool            `json:"tty"`
	SetupScript  string          `json:"setup_script,omitempty"`
	SecretEnv    []string        `json:"secret_env,omitempty"`
	NestedDocker bool            `json:"nested_docker,omitempty"`
	Sudo         bool            `json:"sudo,omitempty"`
	CPUs         int             `json:"cpus"`
	CPULimit     float64         `json:"cpu_limit,omitempty"`
	MemoryMiB    int64           `json:"memory_mib"`
	PidsLimit    int64           `json:"pids_limit,omitempty"`
	Checkouts    []runtime.Mount `json:"checkouts,omitempty"`
	Mounts       []runtime.Mount `json:"mounts,omitempty"`
	Net          int             `json:"net"`
	// Booted is set once a boot was launched: the next cold start builds a
	// fresh root disk. Boots counts them, so a wait on one boot can tell it
	// from the next.
	Booted bool `json:"booted,omitempty"`
	Boots  int  `json:"boots,omitempty"`
	// Saved means the snapshot directory holds the VM's state; the VM reads
	// as exited until Restore.
	Saved bool `json:"saved,omitempty"`
	// CacheRan means the VM ran on its cache disk since the disk last
	// became its pool's newest cache.
	CacheRan bool `json:"cache_ran,omitempty"`
	// ExitCode is the last boot's until the next boot exits.
	Exited   bool `json:"exited,omitempty"`
	ExitCode int  `json:"exit_code,omitempty"`
}

func (e *engine) dir(key string) string { return filepath.Join(e.root, key) }

func (e *engine) path(key, name string) string { return filepath.Join(e.root, key, name) }

// imagesDir is the image store's directory beside the VMs.
const imagesDir = "images"

func checkKey(key string) error {
	if !keyPattern.MatchString(key) || key == imagesDir {
		return fmt.Errorf("vm runtime: creation key %q is not a usable VM name (lowercase letters, digits and dashes)", key)
	}
	return nil
}

// load returns runtime.ErrNotFound for a key with no VM.
func (e *engine) load(key string) (*record, error) {
	if err := checkKey(key); err != nil {
		return nil, fmt.Errorf("%w: %w", runtime.ErrNotFound, err)
	}
	data, err := os.ReadFile(e.path(key, recordFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("vm runtime: VM %q: %w", key, runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("vm runtime: read VM %q: %w", key, err)
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("vm runtime: decode VM %q: %w", key, err)
	}
	return &rec, nil
}

func (e *engine) store(rec *record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("vm runtime: encode VM %q: %w", rec.Key, err)
	}
	return writeFileAtomic(e.path(rec.Key, recordFile), data)
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("vm runtime: write %s: %w", path, err)
	}
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("vm runtime: write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("vm runtime: write %s: %w", path, err)
	}
	return nil
}

func (e *engine) keys() ([]string, error) {
	entries, err := os.ReadDir(e.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vm runtime: list VMs: %w", err)
	}
	var keys []string
	for _, entry := range entries {
		if !entry.IsDir() || checkKey(entry.Name()) != nil {
			continue
		}
		if _, err := os.Stat(e.path(entry.Name(), recordFile)); err == nil {
			keys = append(keys, entry.Name())
		}
	}
	return keys, nil
}
