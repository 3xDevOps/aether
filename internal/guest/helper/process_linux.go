package helper

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/acpkeeper"
	"github.com/3xDevOps/Aether/internal/guest"
	"golang.org/x/sys/unix"
)

// defaultPath is the PATH Docker gives a container whose image sets none.
const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// startupTimeout bounds how long a process started through the helper
// binary may take to execute its program.
const startupTimeout = 10 * time.Second

// maxExited is how many exited processes keep their record and output for
// a late reader, besides the main process, which always does. A record
// whose exit or output a caller has not read yet stays past it, up to
// maxKept.
const (
	maxExited = 32
	maxKept   = 4 * maxExited
)

var (
	errInvalid = errors.New("invalid process")
	errExists  = errors.New("process ID was already used")
	errUnknown = errors.New("no such process")
	errExited  = errors.New("process exited")
	errNoInput = errors.New("process input is closed")
	errNoTerm  = errors.New("process has no terminal")
)

// execError is a program that could not be executed; code is 126 or 127.
type execError struct {
	code int
	err  error
}

func (e *execError) Error() string { return e.err.Error() }

type process struct {
	id       string
	pid      int
	terminal *os.File
	stdinMu  sync.Mutex
	stdin    *os.File
	stdout   *output
	stderr   *output
	done     chan struct{}
	code     int
	// reported is set once a caller has been told the exit.
	reported atomic.Bool
}

// settled reports that no caller has more to read of p: its exit was
// reported and both streams were read to their end.
func (p *process) settled() bool {
	return p.reported.Load() && p.stdout.drained() && p.stderr.drained()
}

// processes is the helper's process table. The helper reaps every child,
// its own and orphans, so it never waits on one process: a spawn holds mu
// from fork until the process is in byPID, and reap takes mu before it
// waits, so no exit is collected before its process is known.
type processes struct {
	mu     sync.Mutex
	byID   map[string]*process
	byPID  map[int]*process
	exited []string
	// cgroup is the directory every spawned process starts in, nil for none.
	cgroup *os.File
}

func newProcesses(cgroup *os.File) *processes {
	return &processes{byID: map[string]*process{}, byPID: map[int]*process{}, cgroup: cgroup}
}

// spawnSpec is a guest.ProcessSpec with the helper's own options: log sends
// both output streams to a file instead of the process's buffers.
type spawnSpec struct {
	guest.ProcessSpec
	log *os.File
}

func (t *processes) spawn(spec spawnSpec) (*process, error) {
	if err := checkSpec(spec.ProcessSpec); err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalid, err)
	}
	cred, home, err := credential(spec.User)
	if err != nil {
		return nil, err
	}
	env := withDefaults(spec.Env, home, spec.TTY != nil)
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.byID[spec.ID]; ok {
		return nil, errExists
	}
	p := &process{
		id: spec.ID, done: make(chan struct{}),
		stdout: newOutput(spec.Lossless), stderr: newOutput(spec.Lossless),
	}
	dir := cmp.Or(spec.Dir, "/")
	program, err := lookPath(spec.Argv[0], envValue(env, "PATH"), dir)
	if info, statErr := os.Stat(dir); err == nil && (statErr != nil || !info.IsDir()) {
		err = &execError{code: 126, err: fmt.Errorf("working directory %s: %w", dir, cmp.Or[error](statErr, syscall.ENOTDIR))}
	}
	if err == nil {
		err = t.start(p, spec, program, dir, env, cred)
	}
	if err != nil {
		var failed *execError
		switch {
		case errors.As(err, &failed):
		case errors.Is(err, syscall.ENOENT):
			failed = &execError{code: 127, err: err}
		default:
			failed = &execError{code: 126, err: err}
		}
		p.code = failed.code
		p.reported.Store(true)
		p.stdout.close()
		p.stderr.close()
		close(p.done)
		t.byID[p.id] = p
		t.retire(p)
		return nil, failed
	}
	t.byID[p.id] = p
	t.byPID[p.pid] = p
	return p, nil
}

// start forks p. Its ends of the pipes or terminal are the only ones the
// helper keeps; the child's are closed once it runs.
func (t *processes) start(p *process, spec spawnSpec, program, dir string, env []string, cred *syscall.Credential) error {
	attr := &syscall.SysProcAttr{Setsid: true, Credential: cred}
	if t.cgroup != nil {
		attr.UseCgroupFD, attr.CgroupFD = true, int(t.cgroup.Fd())
	}
	var child []*os.File
	defer func() {
		for _, f := range child {
			_ = f.Close()
		}
	}()
	var stdoutR, stderrR *os.File
	switch {
	case spec.log != nil:
		null, err := os.Open(os.DevNull)
		if err != nil {
			return err
		}
		child = []*os.File{null, spec.log, spec.log}
	case spec.TTY != nil:
		master, slave, err := acpkeeper.OpenPTY(cred)
		if err != nil {
			return err
		}
		child = []*os.File{slave, slave, slave}
		if err := resize(master, *spec.TTY); err != nil {
			_ = master.Close()
			return err
		}
		attr.Setctty = true
		p.terminal, p.stdin, stdoutR = master, master, master
	default:
		stdinR, stdinW, err := os.Pipe()
		if err != nil {
			return err
		}
		child = append(child, stdinR)
		p.stdin = stdinW
		var outW, errW *os.File
		if stdoutR, outW, err = os.Pipe(); err == nil {
			child = append(child, outW)
			if stderrR, errW, err = os.Pipe(); err == nil {
				child = append(child, errW)
			}
		}
		if err != nil {
			for _, f := range []*os.File{stdinW, stdoutR, stderrR} {
				if f != nil {
					_ = f.Close()
				}
			}
			return err
		}
	}
	path, argv := program, spec.Argv
	var execStatus *os.File
	if spec.NoNewPrivileges {
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		execStatus, child = r, append(child, w)
		path, argv = "/proc/self/exe", append([]string{"aether-internal", "guest", noNewPrivsArg, program}, spec.Argv...)
	}
	fds := make([]uintptr, len(child))
	for i, f := range child {
		fds[i] = f.Fd()
	}
	pid, err := syscall.ForkExec(path, argv, &syscall.ProcAttr{Dir: dir, Env: env, Files: fds, Sys: attr})
	if err == nil && execStatus != nil {
		_ = child[len(child)-1].Close()
		err = trampolineResult(execStatus)
	}
	if err != nil {
		for _, f := range []*os.File{p.stdin, stdoutR, stderrR} {
			if f != nil {
				_ = f.Close()
			}
		}
		p.stdin, p.terminal = nil, nil
		return fmt.Errorf("start %s: %w", program, err)
	}
	p.pid = pid
	if stdoutR != nil {
		go p.stdout.pump(stdoutR, p.done)
	} else {
		p.stdout.close()
	}
	if stderrR != nil {
		go p.stderr.pump(stderrR, p.done)
	} else {
		p.stderr.close()
	}
	return nil
}

// trampolineResult is how the helper binary's exec of the program ended,
// which ForkExec reports for a direct start: its status pipe closes on a
// successful exec and carries the errno of a failed one.
func trampolineResult(status *os.File) error {
	if err := status.SetReadDeadline(time.Now().Add(startupTimeout)); err != nil {
		return err
	}
	buf := make([]byte, 16)
	n, err := status.Read(buf)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("wait for the program to start: %w", err)
	}
	errno, err := strconv.Atoi(string(buf[:n]))
	if err != nil {
		return fmt.Errorf("the program's start reported %q", buf[:n])
	}
	return syscall.Errno(errno)
}

func checkSpec(spec guest.ProcessSpec) error {
	switch {
	case spec.ID == "" || len(spec.ID) > 1024 || strings.ContainsRune(spec.ID, 0):
		return errors.New("process ID must be 1 to 1024 bytes without NUL")
	case len(spec.Argv) == 0 || spec.Argv[0] == "":
		return errors.New("command is required")
	case spec.Dir != "" && !filepath.IsAbs(spec.Dir):
		return errors.New("working directory must be absolute")
	case spec.TTY != nil && (spec.TTY.Cols == 0 || spec.TTY.Rows == 0):
		return errors.New("terminal dimensions must be at least 1")
	}
	for _, s := range append(slices.Clone(spec.Argv), spec.Env...) {
		if strings.ContainsRune(s, 0) {
			return errors.New("command and environment must not contain NUL")
		}
	}
	for _, kv := range spec.Env {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			return fmt.Errorf("environment entry %q is not KEY=VALUE", kv)
		}
	}
	return nil
}

// credential resolves "uid:gid", "uid" or "" (root) the way runc does: the
// home comes from the uid's passwd entry, when it has one, and so do the
// primary and supplementary groups unless the gid is given. A helper that
// is not root, as in tests, runs a process as itself without changing its
// credentials.
func credential(spec string) (*syscall.Credential, string, error) {
	uidText, gidText, hasGID := strings.Cut(cmp.Or(spec, "0"), ":")
	uid, err := strconv.ParseUint(uidText, 10, 32)
	if err != nil {
		return nil, "", fmt.Errorf("%w: user %q is not a numeric uid:gid", errInvalid, spec)
	}
	home, gid, groups := "/", uint64(0), []uint32(nil)
	if u, lookupErr := user.LookupId(uidText); lookupErr == nil {
		home = cmp.Or(u.HomeDir, "/")
		gid, _ = strconv.ParseUint(u.Gid, 10, 32)
		ids, _ := u.GroupIds()
		for _, id := range ids {
			if g, parseErr := strconv.ParseUint(id, 10, 32); parseErr == nil && !hasGID {
				groups = append(groups, uint32(g))
			}
		}
	}
	if hasGID {
		if gid, err = strconv.ParseUint(gidText, 10, 32); err != nil {
			return nil, "", fmt.Errorf("%w: user %q is not a numeric uid:gid", errInvalid, spec)
		}
	}
	if os.Getuid() != 0 && int(uid) == os.Getuid() && int(gid) == os.Getgid() {
		return nil, home, nil
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}, home, nil
}

// withDefaults adds what Docker adds to a process environment that lacks
// it: HOME, PATH, and TERM on a terminal.
func withDefaults(env []string, home string, tty bool) []string {
	env = slices.Clone(env)
	for _, kv := range [][2]string{{"HOME", home}, {"PATH", defaultPath}, {"TERM", "xterm"}} {
		if kv[0] == "TERM" && !tty {
			continue
		}
		if !slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, kv[0]+"=") }) {
			env = append(env, kv[0]+"="+kv[1])
		}
	}
	return env
}

func envValue(env []string, key string) string {
	for _, kv := range slices.Backward(env) {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v
		}
	}
	return ""
}

// lookPath finds name on the process's PATH, not the helper's, and a
// relative name in the process's working directory, dir.
func lookPath(name, path, dir string) (string, error) {
	if strings.Contains(name, "/") {
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		return name, executable(name)
	}
	// A match that cannot run is the answer when nothing later on PATH can.
	var denied error
	for _, d := range filepath.SplitList(path) {
		candidate := filepath.Join(cmp.Or(d, "."), name)
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(dir, candidate)
		}
		err := executable(candidate)
		if err == nil {
			return candidate, nil
		}
		var failed *execError
		if denied == nil && errors.As(err, &failed) && failed.code == 126 {
			denied = err
		}
	}
	if denied != nil {
		return "", denied
	}
	return "", &execError{code: 127, err: fmt.Errorf("%s: executable file not found in $PATH", name)}
}

func executable(name string) error {
	info, err := os.Stat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &execError{code: 127, err: fmt.Errorf("%s: no such file or directory", name)}
	case err != nil:
		return &execError{code: 126, err: err}
	case info.IsDir() || info.Mode()&0o111 == 0:
		return &execError{code: 126, err: fmt.Errorf("%s: permission denied", name)}
	}
	return nil
}

// reap collects every exited child, recording the code of the ones the
// helper spawned.
func (t *processes) reap() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || pid <= 0 {
			return
		}
		p := t.byPID[pid]
		if p == nil {
			continue
		}
		delete(t.byPID, pid)
		p.code = ws.ExitStatus()
		if ws.Signaled() {
			p.code = 128 + int(ws.Signal())
		}
		close(p.done)
		t.retire(p)
		// A write blocked on a pipe its descendants still hold keeps the
		// lock until its deadline, so this must not wait for it.
		go func() { _ = p.closeInput() }()
	}
}

// retire lists p as exited and keeps the newest maxExited exited processes.
// Of the older ones it forgets those no caller still reads, oldest first,
// or past maxKept the oldest of all. The main process is never forgotten.
func (t *processes) retire(p *process) {
	if p.id == guest.MainID {
		return
	}
	t.exited = append(t.exited, p.id)
	for len(t.exited) > maxExited {
		older := t.exited[:len(t.exited)-maxExited]
		i := slices.IndexFunc(older, func(id string) bool { return t.byID[id] == nil || t.byID[id].settled() })
		if i < 0 {
			if len(t.exited) <= maxKept {
				return
			}
			i = 0
		}
		// Closing the output also ends a pump still waiting for a reader of
		// lossless output, which no one can attach to any more.
		if old := t.byID[t.exited[i]]; old != nil {
			old.stdout.close()
			old.stderr.close()
		}
		delete(t.byID, t.exited[i])
		t.exited = slices.Delete(t.exited, i, i+1)
	}
}

func (t *processes) get(id string) (*process, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.byID[id]
	if p == nil {
		return nil, errUnknown
	}
	return p, nil
}

// signal sends sig to a running process. Its PID cannot name another
// process yet: only reap, which holds the same lock, releases it.
func (t *processes) signal(id string, sig syscall.Signal) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.byID[id]
	switch {
	case p == nil:
		return errUnknown
	case t.byPID[p.pid] != p:
		return errExited
	}
	return unix.Kill(p.pid, sig)
}

func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// write writes b to the process's input until ctx ends; a deadline or a
// cancellation interrupts the write without closing the input.
func (p *process) write(ctx context.Context, b []byte) (int, error) {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	stdin := p.stdin
	if stdin == nil {
		return 0, errNoInput
	}
	deadline, _ := ctx.Deadline()
	if err := stdin.SetWriteDeadline(deadline); err != nil {
		return 0, err
	}
	// A cancellation that already started must end before the next write
	// sets its own deadline.
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = stdin.SetWriteDeadline(time.Now())
		close(cancelled)
	})
	n, err := stdin.Write(b)
	if !stop() {
		<-cancelled
	}
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return n, err
}

func (p *process) closeInput() error {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if p.terminal != nil {
		return errNoTerm
	}
	if p.stdin == nil {
		return nil
	}
	err := p.stdin.Close()
	p.stdin = nil
	return err
}

func (p *process) resize(size guest.Size) error {
	if p.terminal == nil {
		return errNoTerm
	}
	return resize(p.terminal, size)
}

// resize goes through the master's raw descriptor: Fd would switch the
// master to blocking mode and end its deadlines.
func resize(master *os.File, size guest.Size) error {
	raw, err := master.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlErr error
	err = raw.Control(func(fd uintptr) {
		ioctlErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: size.Rows, Col: size.Cols})
	})
	return errors.Join(err, ioctlErr)
}
