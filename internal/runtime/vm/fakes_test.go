package vm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/devexec"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

// harness wires an engine to fakes: systemd units that serve a fake Cloud
// Hypervisor API on the VM's real API socket, one fake helper per VM, and a
// fake image store.
type harness struct {
	t      *testing.T
	engine *engine
	units  *fakeUnits
	images *fakeImages
	mu     sync.Mutex
	guests map[string]*fakeHelper
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir, err := os.MkdirTemp("", "vmt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	h := &harness{t: t, images: &fakeImages{}, guests: map[string]*fakeHelper{}}
	h.units = &fakeUnits{t: t, active: map[string]*fakeVMM{}}
	h.engine = h.newEngine(dir)
	t.Cleanup(h.units.stopAll)
	return h
}

// newEngine is a second engine process over the same data directory.
func (h *harness) newEngine(dir string) *engine {
	cfg := Config{DataDir: dir, Images: h.images, Monitor: []string{"/srv/aether-server", MonitorCommand}, Helper: "/srv/aether-server"}
	e := newEngine(cfg, h.units, func(vsock string) (helper, error) {
		return h.guest(filepath.Base(filepath.Dir(vsock))), nil
	})
	e.run = func(context.Context, string, ...string) error { return nil }
	e.natReady = true
	return e
}

func (h *harness) guest(key string) *fakeHelper {
	h.mu.Lock()
	defer h.mu.Unlock()
	g := h.guests[key]
	if g == nil {
		g = &fakeHelper{key: key, units: h.units, procs: map[string]*fakeProc{}}
		h.guests[key] = g
	}
	return g
}

func sessionSpec(key string) runtime.Spec {
	return runtime.Spec{
		Image: "aether/standard:1", Command: []string{"/bin/sh", "-c", "agent"}, TTY: true,
		Env: map[string]string{"AETHER_RUN_ID": key, "HOME": "/root", "TOKEN": "s3cret"}, CreationKey: key,
		Fingerprint: "fp-1", CPULimit: 1.5, MemoryLimitBytes: 2 << 30, PidsLimit: 4096,
	}
}

type fakeImages struct {
	mu        sync.Mutex
	rootDisks int
}

func (f *fakeImages) Kernel() (string, error) { return "/k/vmlinux", nil }

func (f *fakeImages) HelperDisk(_ context.Context, _, path string) (vmimage.Disk, error) {
	return vmimage.Disk{Path: path, Format: "raw"}, os.WriteFile(path, nil, 0o600)
}

func (f *fakeImages) BaseDisk(_ context.Context, ref string) (vmimage.Base, error) {
	if ref == "missing:1" {
		return vmimage.Base{}, errors.New("manifest unknown")
	}
	return vmimage.Base{ID: "sha256:b", Path: "/images/b.raw", Config: vmimage.ImageConfig{
		Env: []string{"PATH=/usr/bin:/bin", "HOME=/image-home"}, WorkingDir: "/srv",
	}}, nil
}

func (f *fakeImages) NewRootDisk(_ context.Context, _ vmimage.Base, path string) (vmimage.Disk, error) {
	f.mu.Lock()
	f.rootDisks++
	f.mu.Unlock()
	return vmimage.Disk{Path: path, Format: "qcow2"}, os.WriteFile(path, nil, 0o600)
}

func (f *fakeImages) NewDataDisk(_ context.Context, path string, _ int64) (vmimage.Disk, error) {
	return vmimage.Disk{Path: path, Format: "raw"}, os.WriteFile(path, nil, 0o600)
}

func (f *fakeImages) roots() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rootDisks
}

// fakeUnits starts a fake VMM per unit: it reads the launch the engine
// wrote and serves Cloud Hypervisor's API on the API socket it names.
type fakeUnits struct {
	t        *testing.T
	mu       sync.Mutex
	active   map[string]*fakeVMM
	started  []startCall
	failNext bool
	stateErr error
}

type startCall struct {
	name       string
	argv       []string
	properties []string
	launch     launch
}

func (u *fakeUnits) Start(_ context.Context, name string, argv, properties []string) error {
	data, err := os.ReadFile(filepath.Join(argv[len(argv)-1], launchFile))
	if err != nil {
		return err
	}
	var l launch
	if err = json.Unmarshal(data, &l); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.started = append(u.started, startCall{name, argv, properties, l})
	if u.failNext {
		u.failNext = false
		return nil
	}
	socket := ""
	for i, arg := range l.Args {
		if arg == "--api-socket" {
			socket = strings.TrimPrefix(l.Args[i+1], "path=")
		}
	}
	vmm, err := startVMM(socket, l.Args, func() { go func() { _ = u.Stop(context.Background(), name) }() })
	if err != nil {
		return err
	}
	u.active[name] = vmm
	return nil
}

func (u *fakeUnits) Stop(_ context.Context, name string) error {
	u.mu.Lock()
	vmm := u.active[name]
	delete(u.active, name)
	u.mu.Unlock()
	if vmm != nil {
		vmm.close()
	}
	return nil
}

func (u *fakeUnits) State(_ context.Context, name string) (unitState, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stateErr != nil {
		return unitState{}, u.stateErr
	}
	if u.active[name] != nil {
		return unitState{Active: true, MainPID: 4242}, nil
	}
	return unitState{}, nil
}

func (u *fakeUnits) stopAll() {
	u.mu.Lock()
	names := make([]string, 0, len(u.active))
	for name := range u.active {
		names = append(names, name)
	}
	u.mu.Unlock()
	for _, name := range names {
		_ = u.Stop(context.Background(), name)
	}
}

func (u *fakeUnits) vmm(key string) *fakeVMM {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.active[unitName(key)]
}

func (u *fakeUnits) last() startCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.started[len(u.started)-1]
}

// fakeVMM is Cloud Hypervisor's REST API with the state transitions the
// engine relies on.
type fakeVMM struct {
	mu       sync.Mutex
	state    string
	calls    []string
	restored bool
	srv      *http.Server
}

func startVMM(socket string, args []string, exit func()) (*fakeVMM, error) {
	_ = os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	v := &fakeVMM{state: "Running"}
	for _, arg := range args {
		if arg == "--restore" {
			v.state, v.restored = "Paused", true
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		endpoint := strings.TrimPrefix(r.URL.Path, "/api/v1/")
		body, _ := io.ReadAll(r.Body)
		v.mu.Lock()
		defer v.mu.Unlock()
		v.calls = append(v.calls, endpoint)
		switch endpoint {
		case "vm.info":
			_ = json.NewEncoder(w).Encode(map[string]string{"state": v.state})
		case "vm.pause":
			v.state = "Paused"
		case "vm.resume":
			v.state = "Running"
		case "vm.snapshot":
			var req struct {
				Destination string `json:"destination_url"`
			}
			_ = json.Unmarshal(body, &req)
			dir := strings.TrimPrefix(req.Destination, "file://")
			_ = os.WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o600)
		case "vm.counters":
			_ = json.NewEncoder(w).Encode(map[string]map[string]uint64{
				"net0": {"rx_bytes": 10, "tx_bytes": 20}, "root": {"read_bytes": 1, "write_bytes": 2}, "data": {"read_bytes": 3, "write_bytes": 4},
			})
		case "vmm.shutdown":
			exit()
		default:
			http.Error(w, "unknown endpoint", http.StatusNotFound)
		}
	})
	v.srv = &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = v.srv.Serve(l) }()
	return v, nil
}

func (v *fakeVMM) close() { _ = v.srv.Close() }

func (v *fakeVMM) called() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.calls...)
}

// fakeHelper is the in-VM helper. Main exits when it is signalled or told to.
type fakeHelper struct {
	key   string
	units *fakeUnits
	mu    sync.Mutex
	boots []guest.BootSpec
	procs map[string]*fakeProc
	// bootErr and output program the next Boot and Spawn.
	bootErr  error
	pingErr  error
	output   map[string]string
	ignoreTE bool
	clock    time.Time
	clockErr error
	bootID   string
	// unresponsive makes calls fail as a guest that stopped answering does.
	unresponsive bool
	stats        guest.Stats
	// finals are dev-exec's own exit records, which outlive the helper's.
	finals map[string]int
}

func (g *fakeHelper) Ping(ctx context.Context) (guest.Info, error) {
	if g.pingErr != nil {
		<-ctx.Done()
		return guest.Info{}, g.pingErr
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return guest.Info{BootID: cmp.Or(g.bootID, "b1"), Protocol: 1}, nil
}

func (g *fakeHelper) Boot(_ context.Context, spec guest.BootSpec) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.boots = append(g.boots, spec)
	if g.bootErr != nil {
		return g.bootErr
	}
	if main := g.procs[guest.MainID]; main == nil || main.exited() {
		g.procs[guest.MainID] = newFakeProc(spec.Main, "", 0, g.ignoreTE)
	}
	return nil
}

func (g *fakeHelper) Spawn(_ context.Context, spec guest.ProcessSpec) (process, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.procs[spec.ID]; ok {
		return nil, errors.New("already exists")
	}
	out := g.output[spec.Argv[0]]
	if len(spec.Argv) >= 8 && spec.Argv[1] == devexec.Command && spec.Argv[2] == "control" {
		out = g.control(spec.Argv[3], spec.Argv[4], spec.Argv[5], spec.Argv[6])
	}
	if strings.HasPrefix(out, "start-error:") {
		return nil, &guest.StartError{Code: 127, Err: errors.New("exec: no such file")}
	}
	p := newFakeProc(spec, out, 0, false)
	g.procs[spec.ID] = p
	if !strings.Contains(strings.Join(spec.Argv, " "), "dev-exec run") {
		p.exit(0)
	}
	return p, nil
}

// control answers like dev-exec's control command; it holds g.mu.
func (g *fakeHelper) control(key, execID, claim, action string) string {
	state := devexec.State{CreationKey: key, ExecID: execID, ClaimToken: claim}
	supervisor := g.procs[execID]
	if code, ok := g.finals[execID]; ok && supervisor == nil {
		state.Exited, state.ExitCode = true, &code
	} else if supervisor == nil {
		state.Error = "no such execution"
	} else {
		if action == "stop" {
			supervisor.exit(143)
		}
		if st, _ := supervisor.Status(context.Background()); st.Exited {
			code := st.ExitCode
			state.Exited, state.ExitCode = true, &code
			if g.finals == nil {
				g.finals = map[string]int{}
			}
			g.finals[execID] = code
		} else {
			state.Running = true
		}
	}
	data, _ := json.Marshal(state)
	return string(data)
}

func (g *fakeHelper) Process(id string) process {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p := g.procs[id]; p != nil {
		return p
	}
	return missingProc{}
}

func (g *fakeHelper) spawned() []guest.ProcessSpec {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []guest.ProcessSpec
	for _, p := range g.procs {
		out = append(out, p.spec)
	}
	return out
}

func (g *fakeHelper) Stats(context.Context) (guest.Stats, error) { return g.stats, nil }

func (g *fakeHelper) SyncClock(_ context.Context, t time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clock = t
	return g.clockErr
}

// PowerOff ends the VM the way a guest poweroff ends Cloud Hypervisor.
func (g *fakeHelper) PowerOff(ctx context.Context) error {
	if g.unresponsive {
		return context.DeadlineExceeded
	}
	return g.units.Stop(ctx, unitName(g.key))
}

func (g *fakeHelper) Close() error { return nil }

type fakeProc struct {
	unresponsive bool
	spec         guest.ProcessSpec
	out          string
	ignoreTE     bool
	mu           sync.Mutex
	signals      []syscall.Signal
	done         chan struct{}
	code         int
	stdin        bool
}

func newFakeProc(spec guest.ProcessSpec, out string, code int, ignoreTerm bool) *fakeProc {
	return &fakeProc{spec: spec, out: out, code: code, ignoreTE: ignoreTerm, done: make(chan struct{})}
}

func (p *fakeProc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *fakeProc) exit(code int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
	default:
		p.code = code
		close(p.done)
	}
}

// Attach serves the programmed output. "hang" never ends stdout, and
// "errflood:" sends stderr past the exec output limit first; both end only
// when the attachment closes.
func (p *fakeProc) Attach(bool) runtime.Attachment {
	stdout, held := io.Pipe()
	a := &fakeAttachment{stdout: stdout, stderr: strings.NewReader(""), closed: func() { _ = held.CloseWithError(io.ErrClosedPipe) }}
	switch p.out {
	case "hang":
	case "errflood:":
		a.stderr = strings.NewReader(strings.Repeat("e", runtime.ExecOutputLimit+1))
	default:
		go func() {
			_, _ = io.WriteString(held, p.out)
			_ = held.Close()
		}()
	}
	return a
}

func (p *fakeProc) CloseStdin(context.Context) error {
	p.mu.Lock()
	p.stdin = true
	p.mu.Unlock()
	return nil
}

func (p *fakeProc) Resize(context.Context, guest.Size) error { return nil }

func (p *fakeProc) Signal(_ context.Context, sig syscall.Signal) error {
	if p.unresponsive {
		return context.DeadlineExceeded
	}
	p.mu.Lock()
	p.signals = append(p.signals, sig)
	ignore := p.ignoreTE && sig == syscall.SIGTERM
	p.mu.Unlock()
	if !ignore {
		p.exit(128 + int(sig))
	}
	return nil
}

func (p *fakeProc) Status(context.Context) (guest.State, error) {
	select {
	case <-p.done:
		return guest.State{Exited: true, ExitCode: p.code}, nil
	default:
		return guest.State{}, nil
	}
}

func (p *fakeProc) Wait(ctx context.Context) (int, error) {
	select {
	case <-p.done:
		return p.code, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

type missingProc struct{}

func (missingProc) Attach(bool) runtime.Attachment               { return &fakeAttachment{} }
func (missingProc) CloseStdin(context.Context) error             { return guest.ErrNotFound }
func (missingProc) Resize(context.Context, guest.Size) error     { return guest.ErrNotFound }
func (missingProc) Signal(context.Context, syscall.Signal) error { return guest.ErrNotFound }
func (missingProc) Status(context.Context) (guest.State, error) {
	return guest.State{}, guest.ErrNotFound
}
func (missingProc) Wait(context.Context) (int, error) { return 0, guest.ErrNotFound }

type fakeAttachment struct {
	stdout, stderr io.Reader
	closed         func()
}

func (a *fakeAttachment) Stdin() io.WriteCloser                    { return nopWriteCloser{} }
func (a *fakeAttachment) Stdout() io.Reader                        { return a.stdout }
func (a *fakeAttachment) Stderr() io.Reader                        { return a.stderr }
func (a *fakeAttachment) Resize(context.Context, uint, uint) error { return nil }
func (a *fakeAttachment) Close() error {
	if a.closed != nil {
		a.closed()
	}
	return nil
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }
