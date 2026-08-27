//go:build integration

package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// e2eRuntime is the fallback runtime.Runtime for hosts without a
// reachable Docker daemon. Each container runs a scripted in-process
// "agent" faithful to the e2e agent script: wait for the supervisor's
// attachment, print a banner, echo one injected line back, write
// result.txt into the bind-mounted checkout, and exit 0. A run may
// register its own script instead (see script), which is how the
// coordination E2E drives an agent that talks to its own mounts.
type e2eRuntime struct {
	mu         sync.Mutex
	seq        int
	containers map[runtime.ID]*e2eContainer
	scripts    map[string]func(*e2eContainer)
}

func newE2ERuntime() *e2eRuntime {
	return &e2eRuntime{
		containers: make(map[runtime.ID]*e2eContainer),
		scripts:    make(map[string]func(*e2eContainer)),
	}
}

// script replaces the default agent behaviour for the run whose launch
// command carries task - the fake harness substitutes the task into its
// argv, so the task is the run's own script key. Registered before the
// run is launched.
func (r *e2eRuntime) script(task string, fn func(*e2eContainer)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripts[task] = fn
}

// container returns the container created for a run, by the creation key
// the scheduler sets to the run ID.
func (r *e2eRuntime) container(key string) *e2eContainer {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.containers {
		if c.spec.CreationKey == key {
			return c
		}
	}
	return nil
}

type e2eContainer struct {
	spec   runtime.Spec
	script func(*e2eContainer)

	stdinR *io.PipeReader
	stdinW *io.PipeWriter

	mu       sync.Mutex
	state    string // created | running | paused | stopped
	atts     []*e2eAttachment
	exit     *runtime.ExitStatus
	done     chan struct{}
	attached chan struct{}
	attOnce  sync.Once
}

func (r *e2eRuntime) Create(_ context.Context, spec runtime.Spec) (runtime.ID, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	stdinR, stdinW := io.Pipe()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := runtime.ID(fmt.Sprintf("e2e-%d", r.seq))
	var script func(*e2eContainer)
	for _, arg := range spec.Command {
		if fn, ok := r.scripts[arg]; ok {
			script = fn
			break
		}
	}
	r.containers[id] = &e2eContainer{
		spec:     spec,
		script:   script,
		stdinR:   stdinR,
		stdinW:   stdinW,
		state:    "created",
		done:     make(chan struct{}),
		attached: make(chan struct{}),
	}
	return id, nil
}

func (r *e2eRuntime) get(id runtime.ID) (*e2eContainer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return nil, fmt.Errorf("e2e runtime: no such container %q", id)
	}
	return c, nil
}

func (r *e2eRuntime) Start(_ context.Context, id runtime.ID) error {
	c, err := r.get(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "created" {
		return fmt.Errorf("e2e runtime: start from state %q", c.state)
	}
	c.state = "running"
	go c.drive()
	return nil
}

// drive is the scripted agent process.
func (c *e2eContainer) drive() {
	select {
	case <-c.attached:
	case <-c.done:
		return
	}
	c.output("agent-ready\r\n")
	if c.script != nil {
		c.script(c)
		c.exitNow(0)
		return
	}
	line, ok := c.readStdinLine()
	if !ok {
		return
	}
	c.output("got:" + line + "\r\n")
	_ = os.WriteFile(filepath.Join(c.spec.WorktreeHostPath, "result.txt"), []byte("hello-from-agent\n"), 0o644)
	c.exitNow(0)
}

// mount finds one of the container's mounts by its container path - how a
// scripted agent reaches a surface it would otherwise open from inside.
func (c *e2eContainer) mount(containerPath string) (runtime.Mount, bool) {
	for _, m := range c.spec.Mounts {
		if m.ContainerPath == containerPath {
			return m, true
		}
	}
	return runtime.Mount{}, false
}

// output emits agent PTY bytes to every open attachment.
func (c *e2eContainer) output(s string) {
	c.mu.Lock()
	atts := slices.Clone(c.atts)
	c.mu.Unlock()
	for _, a := range atts {
		_, _ = a.pw.Write([]byte(s))
	}
}

// readStdinLine reads one \r- or \n-terminated line from the agent's
// stdin, false when stdin closed first.
func (c *e2eContainer) readStdinLine() (string, bool) {
	var line bytes.Buffer
	buf := make([]byte, 1)
	for {
		n, err := c.stdinR.Read(buf)
		if n > 0 {
			if buf[0] == '\r' || buf[0] == '\n' {
				return line.String(), true
			}
			line.WriteByte(buf[0])
		}
		if err != nil {
			return "", false
		}
	}
}

// exitNow ends the main process, EOF-ing every attachment. Idempotent.
func (c *e2eContainer) exitNow(code int) {
	c.mu.Lock()
	if c.exit != nil {
		c.mu.Unlock()
		return
	}
	c.exit = &runtime.ExitStatus{Code: code}
	c.state = "stopped"
	atts := slices.Clone(c.atts)
	close(c.done)
	c.mu.Unlock()
	_ = c.stdinR.Close()
	for _, a := range atts {
		_ = a.pw.Close()
	}
}

func (r *e2eRuntime) Pause(_ context.Context, id runtime.ID) error {
	c, err := r.get(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "running" {
		return fmt.Errorf("e2e runtime: pause from state %q", c.state)
	}
	c.state = "paused"
	return nil
}

func (r *e2eRuntime) Resume(_ context.Context, id runtime.ID) error {
	c, err := r.get(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "paused" {
		return fmt.Errorf("e2e runtime: resume from state %q", c.state)
	}
	c.state = "running"
	return nil
}

func (r *e2eRuntime) Stop(_ context.Context, id runtime.ID, _ time.Duration) error {
	c, err := r.get(id)
	if err != nil {
		return err
	}
	c.exitNow(137)
	return nil
}

func (r *e2eRuntime) Destroy(_ context.Context, id runtime.ID) error {
	r.mu.Lock()
	c, ok := r.containers[id]
	delete(r.containers, id)
	r.mu.Unlock()
	if ok {
		c.exitNow(137)
	}
	return nil
}

func (r *e2eRuntime) Attach(_ context.Context, id runtime.ID) (runtime.Attachment, error) {
	c, err := r.get(id)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exit != nil {
		return nil, errors.New("e2e runtime: container is not running")
	}
	pr, pw := io.Pipe()
	a := &e2eAttachment{c: c, pr: pr, pw: pw}
	c.atts = append(c.atts, a)
	c.attOnce.Do(func() { close(c.attached) })
	return a, nil
}

func (r *e2eRuntime) Wait(ctx context.Context, id runtime.ID) (runtime.ExitStatus, error) {
	c, err := r.get(id)
	if err != nil {
		return runtime.ExitStatus{}, err
	}
	select {
	case <-ctx.Done():
		return runtime.ExitStatus{}, ctx.Err()
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return *c.exit, nil
	}
}

func (r *e2eRuntime) FindByCreationKey(_ context.Context, key string) (runtime.ID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.containers {
		if c.spec.CreationKey != "" && c.spec.CreationKey == key {
			return id, nil
		}
	}
	return "", runtime.ErrNotFound
}

// BuildImage is a no-op: the e2e runtime runs no real images, so a
// "built" tag needs no state for Create to accept it later.
func (r *e2eRuntime) BuildImage(context.Context, string, string, io.Writer) error {
	return nil
}

// RemoveImage is a no-op for the same reason; a missing tag is not an
// error, matching the Docker implementation.
func (r *e2eRuntime) RemoveImage(context.Context, string) error {
	return nil
}

type e2eAttachment struct {
	c  *e2eContainer
	pr *io.PipeReader
	pw *io.PipeWriter

	mu     sync.Mutex
	closed bool
}

// e2eStdin keeps the container's stdin open across attachment closes, per
// the Attachment contract.
type e2eStdin struct{ c *e2eContainer }

func (w e2eStdin) Write(p []byte) (int, error) { return w.c.stdinW.Write(p) }
func (e2eStdin) Close() error                  { return nil }

func (a *e2eAttachment) Stdin() io.WriteCloser { return e2eStdin{a.c} }
func (a *e2eAttachment) Stdout() io.Reader     { return a.pr }
func (a *e2eAttachment) Stderr() io.Reader     { return bytes.NewReader(nil) }

func (a *e2eAttachment) Resize(context.Context, uint, uint) error { return nil }

func (a *e2eAttachment) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	return a.pw.Close()
}
