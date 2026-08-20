package scheduler

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

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// fakeRuntime is an in-memory runtime.Runtime faithful to the Docker
// implementation's observable behavior: sequential attachments, Wait
// blocking until exit, Stop escalating to a signal exit code, Attach
// failing on stopped containers, idempotent Destroy.
type fakeRuntime struct {
	mu         sync.Mutex
	seq        int
	containers map[runtime.ID]*fakeContainer
	createErr  error
	startErr   error
	waitErr    error
	createHook func() // runs at the top of Create; set before any Create call
	attaches   int
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{containers: make(map[runtime.ID]*fakeContainer)}
}

type fakeContainer struct {
	id   runtime.ID
	spec runtime.Spec

	mu    sync.Mutex
	state string // created | running | paused | stopped
	atts  []*fakeAttachment
	stdin bytes.Buffer
	exit  *runtime.ExitStatus
	done  chan struct{}
}

// output emits agent PTY bytes to every open attachment.
func (c *fakeContainer) output(s string) {
	c.mu.Lock()
	atts := slices.Clone(c.atts)
	c.mu.Unlock()
	for _, a := range atts {
		_, _ = a.pw.Write([]byte(s))
	}
}

// exitNow ends the main process with the given code, EOF-ing every
// attachment. Idempotent.
func (c *fakeContainer) exitNow(code int) {
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
	for _, a := range atts {
		_ = a.pw.Close()
	}
}

func (c *fakeContainer) stdinString() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdin.String()
}

func (c *fakeContainer) currentState() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (r *fakeRuntime) Create(_ context.Context, spec runtime.Spec) (runtime.ID, error) {
	if r.createHook != nil {
		r.createHook()
	}
	if err := spec.Validate(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return "", r.createErr
	}
	r.seq++
	id := runtime.ID(fmt.Sprintf("fc-%d", r.seq))
	r.containers[id] = &fakeContainer{id: id, spec: spec, state: "created", done: make(chan struct{})}
	return id, nil
}

func (r *fakeRuntime) get(id runtime.ID) (*fakeContainer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return nil, fmt.Errorf("fake runtime: no such container %q: %w", id, runtime.ErrNotFound)
	}
	return c, nil
}

func (r *fakeRuntime) Start(_ context.Context, id runtime.ID) error {
	if r.startErr != nil {
		return r.startErr
	}
	c, err := r.get(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "created" {
		return fmt.Errorf("fake runtime: start from state %q", c.state)
	}
	c.state = "running"
	return nil
}

func (r *fakeRuntime) Pause(_ context.Context, id runtime.ID) error {
	c, err := r.get(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "running" {
		return fmt.Errorf("fake runtime: pause from state %q", c.state)
	}
	c.state = "paused"
	return nil
}

func (r *fakeRuntime) Resume(_ context.Context, id runtime.ID) error {
	c, err := r.get(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "paused" {
		return fmt.Errorf("fake runtime: resume from state %q", c.state)
	}
	c.state = "running"
	return nil
}

func (r *fakeRuntime) Stop(_ context.Context, id runtime.ID, _ time.Duration) error {
	c, err := r.get(id)
	if err != nil {
		return err
	}
	c.exitNow(137)
	return nil
}

func (r *fakeRuntime) Destroy(_ context.Context, id runtime.ID) error {
	r.mu.Lock()
	c, ok := r.containers[id]
	delete(r.containers, id)
	r.mu.Unlock()
	if ok {
		c.exitNow(137)
	}
	return nil
}

func (r *fakeRuntime) Attach(_ context.Context, id runtime.ID) (runtime.Attachment, error) {
	r.mu.Lock()
	r.attaches++
	r.mu.Unlock()
	c, err := r.get(id)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exit != nil {
		return nil, errors.New("fake runtime: container is not running")
	}
	pr, pw := io.Pipe()
	a := &fakeAttachment{c: c, pr: pr, pw: pw}
	c.atts = append(c.atts, a)
	return a, nil
}

func (r *fakeRuntime) Wait(ctx context.Context, id runtime.ID) (runtime.ExitStatus, error) {
	if r.waitErr != nil {
		return runtime.ExitStatus{}, r.waitErr
	}
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

func (r *fakeRuntime) FindByCreationKey(_ context.Context, key string) (runtime.ID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.containers {
		if c.spec.CreationKey != "" && c.spec.CreationKey == key {
			return id, nil
		}
	}
	return "", fmt.Errorf("fake runtime: creation key %q: %w", key, runtime.ErrNotFound)
}

func (r *fakeRuntime) attachCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attaches
}

// byName finds the container created for a run (Spec.Name is the run ID).
func (r *fakeRuntime) byName(name string) *fakeContainer {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.containers {
		if c.spec.Name == name {
			return c
		}
	}
	return nil
}

func (r *fakeRuntime) allContainers() []*fakeContainer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*fakeContainer, 0, len(r.containers))
	for _, c := range r.containers {
		out = append(out, c)
	}
	return out
}

type fakeAttachment struct {
	c  *fakeContainer
	pr *io.PipeReader
	pw *io.PipeWriter

	mu     sync.Mutex
	cols   uint
	rows   uint
	closed bool
}

type stdinWriter struct{ c *fakeContainer }

func (w stdinWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.c.stdin.Write(p)
}

func (stdinWriter) Close() error { return nil }

func (a *fakeAttachment) Stdin() io.WriteCloser { return stdinWriter{a.c} }
func (a *fakeAttachment) Stdout() io.Reader     { return a.pr }
func (a *fakeAttachment) Stderr() io.Reader     { return bytes.NewReader(nil) }

func (a *fakeAttachment) Resize(_ context.Context, cols, rows uint) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cols, a.rows = cols, rows
	return nil
}

func (a *fakeAttachment) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	return a.pw.Close()
}

// fakeGit implements the GitEngine seam with real checkout directories
// (so relaunch's existence check works) and recorded git operations.
type fakeGit struct {
	root string

	mu                sync.Mutex
	commits           map[domain.RunID][]string
	published         map[domain.RunID]int
	watching          map[domain.RunID]domain.SessionID
	lastFile          map[domain.RunID]time.Time
	bases             map[domain.RunID]string
	workspaceByRun    map[domain.RunID]domain.WorkspaceID
	branchByRun       map[domain.RunID]string
	publishedBranches map[domain.WorkspaceID]map[string]bool
	branchLookupErr   error
	createErr         error
	commitHook        func(run domain.RunID, message string) // runs at the top of CommitAll
}

func newFakeGit(root string) *fakeGit {
	return &fakeGit{
		root:              root,
		commits:           make(map[domain.RunID][]string),
		published:         make(map[domain.RunID]int),
		watching:          make(map[domain.RunID]domain.SessionID),
		lastFile:          make(map[domain.RunID]time.Time),
		bases:             make(map[domain.RunID]string),
		workspaceByRun:    make(map[domain.RunID]domain.WorkspaceID),
		branchByRun:       make(map[domain.RunID]string),
		publishedBranches: make(map[domain.WorkspaceID]map[string]bool),
	}
}

func (g *fakeGit) checkoutPath(run domain.RunID) string {
	return filepath.Join(g.root, string(run))
}

func (g *fakeGit) CreateRunCheckout(_ context.Context, ws domain.WorkspaceID, run domain.RunID, baseBranch, _ string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.createErr != nil {
		return "", "", g.createErr
	}
	path := g.checkoutPath(run)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", "", err
	}
	branch := "aether/run-" + string(run)
	g.bases[run] = baseBranch
	g.workspaceByRun[run] = ws
	g.branchByRun[run] = branch
	return path, branch, nil
}

func (g *fakeGit) WorkspaceBranchExists(_ context.Context, ws domain.WorkspaceID, branch string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.branchLookupErr != nil {
		return false, g.branchLookupErr
	}
	return g.publishedBranches[ws][branch], nil
}

func (g *fakeGit) baseBranchFor(run domain.RunID) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.bases[run]
}

func (g *fakeGit) CommitAll(_ context.Context, run domain.RunID, message string) (string, error) {
	if g.commitHook != nil {
		g.commitHook(run, message)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.commits[run] = append(g.commits[run], message)
	return fmt.Sprintf("commit-%d", len(g.commits[run])), nil
}

func (g *fakeGit) PublishRunBranch(_ context.Context, run domain.RunID) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.published[run]++
	ws, workspaceOK := g.workspaceByRun[run]
	branch, branchOK := g.branchByRun[run]
	if workspaceOK && branchOK {
		if g.publishedBranches[ws] == nil {
			g.publishedBranches[ws] = make(map[string]bool)
		}
		g.publishedBranches[ws][branch] = true
	}
	return "tip", nil
}

func (g *fakeGit) RemoveRunCheckout(_ context.Context, run domain.RunID) error {
	return os.RemoveAll(g.checkoutPath(run))
}

func (g *fakeGit) StartDiffWatch(_ context.Context, session domain.SessionID, run domain.RunID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.watching[run] = session
	return nil
}

func (g *fakeGit) StopDiffWatch(run domain.RunID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.watching, run)
}

func (g *fakeGit) LastFileChange(run domain.RunID) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, ok := g.lastFile[run]
	return t, ok
}

func (g *fakeGit) touch(run domain.RunID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastFile[run] = time.Now().UTC()
}

func (g *fakeGit) commitsFor(run domain.RunID) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.commits[run])
}

func (g *fakeGit) unpublishBranch(ws domain.WorkspaceID, branch string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.publishedBranches[ws], branch)
}

func (g *fakeGit) publishedCount(run domain.RunID) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.published[run]
}

func (g *fakeGit) checkoutCount() int {
	entries, err := os.ReadDir(g.root)
	if err != nil {
		return 0
	}
	return len(entries)
}

// fakePTY implements the PTYHost seam. Like the real ptyhost it takes
// ownership of the attachment and pumps its Stdout, recording the time of
// the last byte read.
type fakePTY struct {
	mu       sync.Mutex
	sessions map[domain.RunID]*fakePTYSession
	injects  []fakeInject
}

type fakePTYSession struct {
	att runtime.Attachment

	mu    sync.Mutex
	out   bytes.Buffer
	last  time.Time
	ended bool
}

type fakeInject struct {
	run     domain.RunID
	name    string
	color   string
	message string
}

var errFakeNoSession = errors.New("fake ptyhost: no session for run")

func newFakePTY() *fakePTY {
	return &fakePTY{sessions: make(map[domain.RunID]*fakePTYSession)}
}

func (p *fakePTY) StartSession(_ context.Context, run domain.RunID, att runtime.Attachment) error {
	sess := &fakePTYSession{att: att}
	p.mu.Lock()
	p.sessions[run] = sess
	p.mu.Unlock()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := att.Stdout().Read(buf)
			if n > 0 {
				sess.mu.Lock()
				sess.out.Write(buf[:n])
				sess.last = time.Now().UTC()
				sess.mu.Unlock()
			}
			if err != nil {
				sess.mu.Lock()
				sess.ended = true
				sess.mu.Unlock()
				return
			}
		}
	}()
	return nil
}

func (p *fakePTY) StopSession(_ context.Context, run domain.RunID) error {
	p.mu.Lock()
	sess, ok := p.sessions[run]
	p.mu.Unlock()
	if !ok {
		return errFakeNoSession
	}
	return sess.att.Close()
}

func (p *fakePTY) LastOutput(run domain.RunID) (time.Time, bool) {
	p.mu.Lock()
	sess, ok := p.sessions[run]
	p.mu.Unlock()
	if !ok {
		return time.Time{}, false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.last, true
}

func (p *fakePTY) Inject(_ context.Context, run domain.RunID, actorName, actorColor, message string) error {
	p.mu.Lock()
	sess, ok := p.sessions[run]
	if ok {
		p.injects = append(p.injects, fakeInject{run: run, name: actorName, color: actorColor, message: message})
	}
	p.mu.Unlock()
	if !ok {
		return errFakeNoSession
	}
	sess.mu.Lock()
	sess.last = time.Now().UTC()
	sess.mu.Unlock()
	_, err := sess.att.Stdin().Write([]byte(message + "\r"))
	return err
}

func (p *fakePTY) session(run domain.RunID) *fakePTYSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[run]
}

func (p *fakePTY) injected() []fakeInject {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.injects)
}

func (s *fakePTYSession) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}
