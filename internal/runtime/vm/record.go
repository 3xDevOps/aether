package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/guest"
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
	rootCopy    = "root-copy.img"
	browserDisk = "browser.img"
	dataDisk    = "data.img"
	cacheDisk   = "cache.qcow2"
	// stateDiskFile is the session's state disk, in the directory its state
	// mounts name.
	stateDiskFile = "disk.img"
	consoleLog    = "console.log"
	eventsFile    = "events.json"
	monitorLog    = "monitor.log"
	snapshotDir   = "snapshot"
)

// offline is the Net of a VM with no network device.
const offline = -1

// keyPattern keeps a creation key usable as a directory, a systemd unit name
// and a guest hostname.
var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// record is everything a VM needs to boot again and to answer Inspect
// without reaching the guest. Env is the image's environment overlaid with
// the spec's, which is what a container reports.
type record struct {
	Key string `json:"key"`
	// CreationKey is the spec's, which a VM of one of the kinds names
	// differently.
	CreationKey  string          `json:"creation_key,omitempty"`
	Fingerprint  string          `json:"fingerprint,omitempty"`
	Image        string          `json:"image"`
	Base         vmimage.Base    `json:"base"`
	Root         vmimage.Disk    `json:"root"`
	Data         vmimage.Disk    `json:"data"`
	Cache        vmimage.Disk    `json:"cache,omitzero"`
	State        vmimage.Disk    `json:"state,omitzero"`
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
	// CheckoutDisks are the Mounts whose checkouts live on disks of their
	// own.
	CheckoutDisks []checkoutDisk `json:"checkout_disks,omitempty"`
	// Net is the VM's network index, or offline.
	Net    int  `json:"net"`
	Egress bool `json:"egress,omitempty"`
	// KeepRoot keeps the root disk across cold starts.
	KeepRoot  bool           `json:"keep_root,omitempty"`
	Lifetime  time.Duration  `json:"lifetime,omitempty"`
	OwnedDirs []string       `json:"owned_dirs,omitempty"`
	Serves    []guest.Socket `json:"serves,omitempty"`
	// Browser is the development browser's disk, attached read-only; empty
	// while its image was not built yet. A session VM takes the current
	// image's at every cold start.
	Browser      vmimage.Disk `json:"browser"`
	BrowserImage string       `json:"browser_image,omitempty"`
	BrowserEnv   []string     `json:"browser_env,omitempty"`
	// Companion is the browser companion created in the VM, if any.
	Companion *companion `json:"companion,omitempty"`
	// Booted is set once a boot was launched: the next cold start builds a
	// fresh root disk. Boots counts them, so a wait on one boot can tell it
	// from the next.
	Booted bool `json:"booted,omitempty"`
	Boots  int  `json:"boots,omitempty"`
	// Saved means the snapshot directory holds the VM's state; the VM reads
	// as exited until Restore.
	Saved bool `json:"saved,omitempty"`
	// Frozen means Pause froze the session's processes, which stay frozen
	// through a save and restore until Resume or the next boot.
	Frozen bool `json:"frozen,omitempty"`
	// CacheRan means the VM ran on its cache disk since the disk last
	// became its pool's newest cache. CacheSynced means the disk holds every
	// write the guest made: the VM powered off cleanly, or was saved after
	// the guest synced.
	CacheRan    bool `json:"cache_ran,omitempty"`
	CacheSynced bool `json:"cache_synced,omitempty"`
	// ExitCode is the last boot's until the next boot exits.
	Exited   bool      `json:"exited,omitempty"`
	ExitCode int       `json:"exit_code,omitempty"`
	ExitedAt time.Time `json:"exited_at,omitzero"`
}

// session reports whether the VM runs a session, which carries its run ID,
// the VM's key, in AETHER_RUN_ID, as isSession matches its spec.
func (r *record) session() bool {
	return runtime.EnvValue(r.Env, coordtransport.RunIDEnv) == r.Key
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
