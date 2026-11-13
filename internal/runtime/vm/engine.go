package vm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

type Images interface {
	Kernel() (string, error)
	HelperDisk(ctx context.Context, binary, path string) (vmimage.Disk, error)
	BaseDisk(ctx context.Context, ref string) (vmimage.Base, error)
	NewRootDisk(ctx context.Context, base vmimage.Base, path string) (vmimage.Disk, error)
	NewDataDisk(ctx context.Context, path string, size int64) (vmimage.Disk, error)
	NewCacheDisk(ctx context.Context, pool, path string) (vmimage.Disk, error)
	SnapshotCache(ctx context.Context, disk string) (string, error)
	PromoteCache(ctx context.Context, pool, snapshot string) error
}

const (
	// dataDiskBytes is sparse: it costs only what the guest writes.
	dataDiskBytes = 128 << 30
	minMemoryMiB  = 256
	bootTimeout   = 90 * time.Second
	agentOOMScore = 500
	// unitOverheadMiB is what a VM's unit may hold beyond the guest's
	// memory: Cloud Hypervisor, virtiofsd, and the monitor with the file
	// service, whose requests a guest can fill.
	unitOverheadMiB = 512
	// cmdlineBase boots the read-only helper disk, whose /init is the helper.
	// Page reporting order 0 lets freed guest page cache back to the host;
	// at the default order it stalls with most of it still held. Cloud
	// Hypervisor never exits on a guest panic, so panic=-1 makes a dead
	// helper reboot the guest, which its event log reports.
	cmdlineBase = "root=/dev/vda ro rootfstype=ext4 init=/init console=ttyS0 quiet panic=-1 page_reporting.page_reporting_order=0"
)

type engine struct {
	cfg   Config
	root  string
	units units
	dial  func(vsockPath string) (helper, error)
	run   func(ctx context.Context, name string, args ...string) error

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	helpers map[string]helper
	booting map[string]bool
	fresh   map[string]bool
	events  map[string]runtime.MemoryEvents

	netMu    sync.Mutex
	reserved map[int]bool

	promotions sync.WaitGroup
	natReady   bool
}

func newEngine(cfg Config, u units, dial func(string) (helper, error)) *engine {
	return &engine{
		cfg: cfg, root: filepath.Join(cfg.DataDir, "vm"), units: u, dial: dial, run: runCommand,
		locks: map[string]*sync.Mutex{}, helpers: map[string]helper{}, booting: map[string]bool{},
		fresh: map[string]bool{}, events: map[string]runtime.MemoryEvents{}, reserved: map[int]bool{},
	}
}

func runCommand(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("vm runtime: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (e *engine) lock(key string) func() {
	e.mu.Lock()
	l := e.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		e.locks[key] = l
	}
	e.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// helper returns the key's client, dialing once per VM lifetime; the client
// redials by itself across a save and restore.
func (e *engine) helper(key string) (helper, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if h := e.helpers[key]; h != nil {
		return h, nil
	}
	h, err := e.dial(e.path(key, vsockSocket))
	if err != nil {
		return nil, fmt.Errorf("vm runtime: connect to the helper of VM %s: %w", key, err)
	}
	e.helpers[key] = h
	return h, nil
}

func (e *engine) forgetHelper(key string) {
	e.mu.Lock()
	h := e.helpers[key]
	delete(e.helpers, key)
	e.mu.Unlock()
	if h != nil {
		_ = h.Close()
	}
}

func (e *engine) setFlag(m map[string]bool, key string, on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if on {
		m[key] = true
	} else {
		delete(m, key)
	}
}

func (e *engine) flag(m map[string]bool, key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return m[key]
}

func (e *engine) create(ctx context.Context, spec runtime.Spec) (_ runtime.ID, err error) {
	if err = spec.Validate(); err != nil {
		return "", err
	}
	key := spec.CreationKey
	if err = checkKey(key); err != nil {
		return "", err
	}
	if spec.Egress {
		return "", errors.New("vm runtime: egress control (the Isolated tier) is not supported in session VMs until work item 5")
	}
	memMiB, err := memoryMiB(spec.MemoryLimitBytes)
	if err != nil {
		return "", err
	}
	unlock := e.lock(key)
	defer unlock()
	if _, err = os.Stat(e.dir(key)); err == nil {
		return "", fmt.Errorf("vm runtime: a VM named %s already exists", key)
	}
	base, err := e.cfg.Images.BaseDisk(ctx, spec.Image)
	if err != nil {
		return "", fmt.Errorf("vm runtime: base disk for %s: %w", spec.Image, err)
	}
	index, err := e.allocateNet()
	if err != nil {
		return "", err
	}
	defer e.releaseNet(index)
	if err = os.MkdirAll(e.dir(key), 0o700); err != nil {
		return "", fmt.Errorf("vm runtime: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(e.dir(key))
		}
	}()
	root, err := e.cfg.Images.NewRootDisk(ctx, base, e.path(key, rootDisk))
	if err != nil {
		return "", fmt.Errorf("vm runtime: root disk: %w", err)
	}
	data, err := e.cfg.Images.NewDataDisk(ctx, e.path(key, dataDisk), dataDiskBytes)
	if err != nil {
		return "", fmt.Errorf("vm runtime: data disk: %w", err)
	}
	var cache vmimage.Disk
	if m, ok := cacheMount(spec.Mounts); ok {
		if cache, err = e.cfg.Images.NewCacheDisk(ctx, cachePool(m), e.path(key, cacheDisk)); err != nil {
			return "", fmt.Errorf("vm runtime: cache disk: %w", err)
		}
	}
	cpus, limit := vcpus(spec.CPULimit)
	rec := &record{
		Key: key, Fingerprint: spec.Fingerprint, Image: spec.Image, Base: base, Root: root, Data: data, Cache: cache,
		User:       spec.User,
		Env:        overrideEnv(base.Config.Env, runtime.EnvList(runtime.TrustCheckouts(spec.Env, spec.WorktreeMountPath, spec.ExtraWorktrees))),
		WorkingDir: cmp.Or(spec.WorkingDir, base.Config.WorkingDir, "/"), Command: spec.Command, TTY: spec.TTY,
		SetupScript: spec.SetupScript, SecretEnv: spec.SecretEnv, NestedDocker: spec.NestedDocker, Sudo: spec.Sudo,
		CPUs: cpus, CPULimit: limit, MemoryMiB: memMiB, PidsLimit: spec.PidsLimit, Checkouts: checkouts(spec), Mounts: spec.Mounts, Net: index,
	}
	if err = e.store(rec); err != nil {
		return "", err
	}
	return runtime.ID(idPrefix + key), nil
}

// vcpus rounds a fractional CPU limit up to whole vCPUs and keeps the limit
// itself as the unit's CPU quota; no limit means every host CPU.
func vcpus(limit float64) (int, float64) {
	host := goruntime.NumCPU()
	if limit <= 0 {
		return host, 0
	}
	return min(host, max(1, int(math.Ceil(limit)))), limit
}

func memoryMiB(limit int64) (int64, error) {
	if limit <= 0 {
		total, err := hostMemoryBytes()
		if err != nil {
			return 0, err
		}
		return total >> 20, nil
	}
	mib := (limit + (1<<20 - 1)) >> 20
	if mib < minMemoryMiB {
		return 0, fmt.Errorf("vm runtime: a memory limit of %d bytes is below the %d MiB a session VM needs to boot", limit, minMemoryMiB)
	}
	return mib, nil
}

func hostMemoryBytes() (int64, error) {
	var kb int64
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("vm runtime: host memory: %w", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			if _, err := fmt.Sscanf(strings.TrimSpace(rest), "%d kB", &kb); err != nil {
				return 0, fmt.Errorf("vm runtime: parse MemTotal: %w", err)
			}
			return kb << 10, nil
		}
	}
	return 0, errors.New("vm runtime: /proc/meminfo has no MemTotal")
}

// start cold-starts a stopped VM; on a running one it only makes sure Main
// was started, as a redundant docker start does.
func (e *engine) start(ctx context.Context, key string) (err error) {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.load(key)
	if err != nil {
		return err
	}
	st, err := e.units.State(ctx, unitName(key))
	if err != nil {
		return err
	}
	if st.Active {
		return e.boot(ctx, rec)
	}
	if rec.Saved {
		if err = os.RemoveAll(e.path(key, snapshotDir)); err != nil {
			return fmt.Errorf("vm runtime: discard saved state: %w", err)
		}
		rec.Saved = false
	}
	if rec.Booted {
		if rec.Root, err = e.cfg.Images.NewRootDisk(ctx, rec.Base, e.path(key, rootDisk)); err != nil {
			return fmt.Errorf("vm runtime: root disk: %w", err)
		}
	}
	l, err := e.bootLaunch(ctx, rec)
	if err != nil {
		return err
	}
	if err = e.ensureNAT(ctx); err != nil {
		return err
	}
	rec.Booted, rec.Exited = true, false
	rec.CacheRan, rec.CacheSynced = rec.Cache.Path != "", false
	rec.Boots++
	if err = e.store(rec); err != nil {
		return err
	}
	if err = e.launch(ctx, rec, l); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			e.kill(context.WithoutCancel(ctx), key)
		}
	}()
	if err = e.boot(ctx, rec); err != nil {
		return err
	}
	e.setFlag(e.fresh, key, true)
	return nil
}

func (e *engine) bootLaunch(ctx context.Context, rec *record) (launch, error) {
	p, err := e.plan(rec)
	if err != nil {
		return launch{}, err
	}
	if err = linkSockets(e.dir(rec.Key), p.links); err != nil {
		return launch{}, err
	}
	kernel, err := e.cfg.Images.Kernel()
	if err != nil {
		return launch{}, fmt.Errorf("vm runtime: guest kernel: %w", err)
	}
	helperDisk, err := e.cfg.Images.HelperDisk(ctx, e.cfg.Helper, e.path(rec.Key, helperImage))
	if err != nil {
		return launch{}, fmt.Errorf("vm runtime: helper disk: %w", err)
	}
	cmdline, err := guest.Config{
		Hostname: rec.Key, Network: guestNetwork(rec.Net, hostDNS()), Mounts: p.mounts, Sockets: p.sockets,
	}.KernelArgs(cmdlineBase)
	if err != nil {
		return launch{}, fmt.Errorf("vm runtime: kernel command line: %w", err)
	}
	args := []string{
		"--api-socket", "path=" + e.path(rec.Key, apiSocket),
		"--vsock", "cid=3,socket=" + e.path(rec.Key, vsockSocket),
		"--serial", "file=" + e.path(rec.Key, consoleLog), "--console", "off",
		"--event-monitor", "path=" + e.path(rec.Key, eventsFile),
		"--kernel", kernel, "--cmdline", cmdline,
		"--disk", diskArg(helperDisk, "helper") + ",readonly=on", diskArg(rec.Root, "root"), diskArg(rec.Data, "data"),
	}
	if rec.Cache.Path != "" {
		args = append(args, diskArg(rec.Cache, "cache"))
	}
	args = append(args,
		"--cpus", fmt.Sprintf("boot=%d", rec.CPUs),
		"--memory", fmt.Sprintf("size=%dM,shared=on", rec.MemoryMiB),
		"--balloon", "size=0,free_page_reporting=on",
		"--net", "id=net0,"+chNetArg(rec.Net),
	)
	l := launch{Args: args, Shares: p.shares, Home: p.homeConfig(rec.User)}
	if len(p.shares) > 0 {
		l.Args = append(l.Args, "--fs", "tag="+guest.ShareTag+",socket="+e.path(rec.Key, fsSocket))
	}
	return l, nil
}

func diskArg(d vmimage.Disk, id string) string {
	arg := fmt.Sprintf("path=%s,image_type=%s,id=%s,serial=%s", d.Path, d.Format, id, id)
	if d.Format == vmimage.FormatQcow2 {
		arg += ",backing_files=on"
	}
	return arg
}

func (e *engine) launch(ctx context.Context, rec *record, l launch) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("vm runtime: encode launch: %w", err)
	}
	if err := writeFileAtomic(e.path(rec.Key, launchFile), data); err != nil {
		return err
	}
	logPath := e.path(rec.Key, monitorLog)
	properties := []string{
		"Type=exec", "PrivateMounts=yes", "KillMode=control-group", "TimeoutStopSec=15",
		fmt.Sprintf("OOMScoreAdjust=%d", agentOOMScore), fmt.Sprintf("MemoryMax=%dM", rec.MemoryMiB+unitOverheadMiB),
		"StandardOutput=append:" + logPath, "StandardError=append:" + logPath,
	}
	if rec.CPULimit > 0 {
		properties = append(properties, fmt.Sprintf("CPUQuota=%d%%", int(math.Ceil(rec.CPULimit*100))))
	}
	argv := append(append([]string{}, e.cfg.Monitor...), e.dir(rec.Key))
	if err := e.units.Start(ctx, unitName(rec.Key), argv, properties); err != nil {
		return err
	}
	return nil
}

func (e *engine) boot(ctx context.Context, rec *record) error {
	h, err := e.helper(rec.Key)
	if err != nil {
		return err
	}
	if err = e.waitHelper(ctx, rec.Key, h); err != nil {
		return err
	}
	upCtx, stop, _ := e.whileUp(ctx, rec.Key)
	defer stop()
	stopped := func(err error) error {
		if upCtx.Err() != nil && ctx.Err() == nil {
			return fmt.Errorf("vm runtime: VM %s stopped while booting%s", rec.Key, e.logTail(rec.Key))
		}
		return err
	}
	if runtime.Unprivileged(rec.User) {
		allow := ""
		if rec.Sudo {
			allow = "sudo"
		}
		home := cmp.Or(runtime.EnvValue(rec.Env, "HOME"), "/")
		code, out, prepErr := e.runAsRoot(upCtx, h, rec, []string{"/bin/sh", "-c", runtime.UserScript, "aether-user", rec.User, home, allow})
		if prepErr != nil {
			return stopped(fmt.Errorf("vm runtime: prepare user %s: %w", rec.User, prepErr))
		}
		if code != 0 {
			return fmt.Errorf("vm runtime: prepare user %s exited %d: %s", rec.User, code, strings.TrimSpace(out))
		}
	}
	var tty *guest.Size
	if rec.TTY {
		tty = &guest.Size{Cols: 80, Rows: 24}
	}
	e.setFlag(e.booting, rec.Key, true)
	err = h.Boot(upCtx, guest.BootSpec{
		Main: guest.ProcessSpec{
			ID: guest.MainID, Argv: rec.Command, Env: rec.Env, User: rec.User, Dir: rec.WorkingDir, TTY: tty,
			NoNewPrivileges: !rec.Sudo,
		},
		SetupScript: rec.SetupScript, NestedDocker: rec.NestedDocker, PidsLimit: rec.PidsLimit,
	})
	e.setFlag(e.booting, rec.Key, false)
	var setup *guest.SetupError
	if errors.As(err, &setup) {
		return &runtime.SetupError{ExitCode: setup.ExitCode, Output: runtime.SetupOutput(setup.Output, setup.Cut, runtime.SecretValues(rec.SecretEnv, rec.Env))}
	}
	if err != nil {
		return stopped(fmt.Errorf("vm runtime: boot VM %s: %w", rec.Key, err))
	}
	return nil
}

func (e *engine) waitHelper(ctx context.Context, key string, h helper) error {
	ctx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()
	answered := make(chan error, 1)
	var info guest.Info
	go func() {
		var err error
		info, err = h.Ping(ctx)
		answered <- err
	}()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-answered:
			if err != nil {
				return fmt.Errorf("vm runtime: helper of VM %s did not answer: %w%s", key, err, e.logTail(key))
			}
			if info.BootError != "" {
				return fmt.Errorf("vm runtime: VM %s failed to boot: %s", key, info.BootError)
			}
			return nil
		case <-tick.C:
			if st, err := e.units.State(ctx, unitName(key)); err == nil && !st.Active {
				cancel()
				return fmt.Errorf("vm runtime: VM %s stopped while booting%s", key, e.logTail(key))
			}
		}
	}
}

func (e *engine) logTail(key string) string {
	var out strings.Builder
	for _, name := range []string{monitorLog, consoleLog} {
		data, err := os.ReadFile(e.path(key, name))
		if err != nil || len(data) == 0 {
			continue
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		fmt.Fprintf(&out, "\n%s:\n%s", name, strings.Join(lines[max(0, len(lines)-15):], "\n"))
	}
	return out.String()
}
