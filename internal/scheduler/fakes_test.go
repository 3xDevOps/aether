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
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// fakeRuntime is an in-memory runtime.Runtime faithful to the Docker
// implementation's observable behavior: sequential attachments, Wait
// blocking until exit, Stop escalating to a signal exit code, Attach
// failing on stopped containers, idempotent Destroy.
type fakeRuntime struct {
	mu          sync.Mutex
	seq         int
	containers  map[runtime.ID]*fakeContainer
	containerIP string
	createErr   error
	createHook  func()
	startErr    error
	waitErr     error
	startHook   func(c *fakeContainer)
	commitErr   error
	commits     []fakeCommitCall
	// heldImages are tags the fake daemon refuses to remove, as Docker does
	// while a container still uses them.
	heldImages map[string]bool
	// execTTYHook overrides ExecTTY for tests that need to model an
	// immediate shell-executable failure.
	execTTYHook func(context.Context, runtime.ID, []string, string, uint, uint) (runtime.Attachment, error)
	execCalls   []fakeExecTTYCall
	attaches    int
	// images is the fake daemon's local image registry.
	images map[string]string
}

type fakeCommitCall struct {
	id  runtime.ID
	tag string
}

type fakeExecTTYCall struct {
	id      runtime.ID
	argv    []string
	workDir string
	cols    uint
	rows    uint
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{containers: make(map[runtime.ID]*fakeContainer), containerIP: "127.0.0.1"}
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

// exitNow is the test-facing exit: it ends the main process with the
// given code. It rejects state "created", the one state Docker can never
// report as exited, because a container has no process before Start. A
// forged exit there leaves Attach refusing the container as not running,
// so a test that beats the code under test to Start reads that refusal
// instead of the exit code it set.
func (c *fakeContainer) exitNow(code int) {
	c.mu.Lock()
	created := c.state == "created"
	c.mu.Unlock()
	if created {
		panic(fmt.Sprintf("fake runtime: container %s exited before it started; wait for it to be running first", c.id))
	}
	c.endProcess(code)
}

// endProcess records the exit code and EOFs every attachment. Idempotent,
// and reachable from any state: Stop and Destroy tear down containers
// that never started.
func (c *fakeContainer) endProcess(code int) {
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
	if r.startHook != nil {
		go r.startHook(c)
	}
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
	c.endProcess(137)
	return nil
}

func (r *fakeRuntime) Destroy(_ context.Context, id runtime.ID) error {
	r.mu.Lock()
	c, ok := r.containers[id]
	delete(r.containers, id)
	r.mu.Unlock()
	if ok {
		c.endProcess(137)
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
func (r *fakeRuntime) ContainerIP(_ context.Context, id runtime.ID) (string, error) {
	if _, err := r.get(id); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.containerIP == "" {
		return "", fmt.Errorf("fake runtime: container %q has no IP", id)
	}
	return r.containerIP, nil
}

func (r *fakeRuntime) ExecTTY(ctx context.Context, id runtime.ID, argv []string, workDir string, cols, rows uint) (runtime.Attachment, error) {
	r.mu.Lock()
	r.execCalls = append(r.execCalls, fakeExecTTYCall{
		id: id, argv: slices.Clone(argv), workDir: workDir, cols: cols, rows: rows,
	})
	hook := r.execTTYHook
	r.mu.Unlock()
	if hook != nil {
		return hook(ctx, id, argv, workDir, cols, rows)
	}
	return r.attachForExec(ctx, id, argv, workDir, cols, rows)
}

func (r *fakeRuntime) attachForExec(ctx context.Context, id runtime.ID, _ []string, _ string, cols, rows uint) (runtime.Attachment, error) {
	att, err := r.Attach(ctx, id)
	if err != nil {
		return nil, err
	}
	if cols != 0 && rows != 0 {
		if err := att.Resize(ctx, cols, rows); err != nil {
			_ = att.Close()
			return nil, err
		}
	}
	return att, nil
}

func (r *fakeRuntime) execTTYCalls() []fakeExecTTYCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.execCalls)
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

// Commit records a saved image and registers its tag in the fake daemon.
func (r *fakeRuntime) Commit(_ context.Context, id runtime.ID, tag string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.commitErr != nil {
		return r.commitErr
	}
	r.commits = append(r.commits, fakeCommitCall{id: id, tag: tag})
	if r.images == nil {
		r.images = make(map[string]string)
	}
	r.images[tag] = string(id)
	return nil
}

func (r *fakeRuntime) commitCalls() []fakeCommitCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.commits)
}

// ImageExists mirrors the Docker capability probe the scheduler uses for
// saved member environment images.
func (r *fakeRuntime) ImageExists(_ context.Context, tag string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.images[tag]
	return ok, nil
}

// ListImageTags returns the registered tags under repo, like Docker's
// reference filter.
func (r *fakeRuntime) ListImageTags(_ context.Context, repo string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var tags []string
	for tag := range r.images {
		if strings.HasPrefix(tag, repo+":") {
			tags = append(tags, tag)
		}
	}
	return tags, nil
}

func (r *fakeRuntime) hasImage(tag string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.images[tag]
	return ok
}

// RemoveImage forgets a saved member environment tag; a missing tag is not
// an error, matching the Docker implementation.
func (r *fakeRuntime) RemoveImage(_ context.Context, tag string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.heldImages[tag] {
		return fmt.Errorf("fake runtime: image %s is in use by a container", tag)
	}
	delete(r.images, tag)
	return nil
}

func (r *fakeRuntime) holdImage(tag string, held bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.heldImages == nil {
		r.heldImages = make(map[string]bool)
	}
	r.heldImages[tag] = held
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
	watching          map[domain.RunID]domain.WorkspaceID
	lastFile          map[domain.RunID]time.Time
	bases             map[domain.RunID]string
	workspaceByRun    map[domain.RunID]domain.WorkspaceID
	branchByRun       map[domain.RunID]string
	publishedBranches map[domain.WorkspaceID]map[string]bool
	branchLookupErr   error
	createErr         error
	createHook        func(run domain.RunID)
	commitHook        func(run domain.RunID, message string) // runs at the top of CommitAll
}

func newFakeGit(root string) *fakeGit {
	return &fakeGit{
		root:              root,
		commits:           make(map[domain.RunID][]string),
		published:         make(map[domain.RunID]int),
		watching:          make(map[domain.RunID]domain.WorkspaceID),
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
	hook := g.createHook
	if hook != nil {
		hook(run)
	}
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

func (g *fakeGit) StartDiffWatch(_ context.Context, workspace domain.WorkspaceID, run domain.RunID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.watching[run] = workspace
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

// watchingFor reports the workspace scope StartDiffWatch was called with,
// and whether a watch is currently active for the run.
func (g *fakeGit) watchingFor(run domain.RunID) (domain.WorkspaceID, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ws, ok := g.watching[run]
	return ws, ok
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
	mu              sync.Mutex
	sessions        map[ptyhost.SessionKey]*fakePTYSession
	injects         []fakeInject
	stoppedPrefixes []string
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
	return &fakePTY{sessions: make(map[ptyhost.SessionKey]*fakePTYSession)}
}

func (p *fakePTY) StartSession(_ context.Context, key ptyhost.SessionKey, att runtime.Attachment) error {
	sess := &fakePTYSession{att: att}
	p.mu.Lock()
	p.sessions[key] = sess
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

func (p *fakePTY) StopSession(_ context.Context, key ptyhost.SessionKey) error {
	p.mu.Lock()
	sess, ok := p.sessions[key]
	p.mu.Unlock()
	if !ok {
		return errFakeNoSession
	}
	return sess.att.Close()
}

func (p *fakePTY) RemoveRunTranscripts(_ context.Context, _ domain.RunID) error {
	return nil
}
func (p *fakePTY) ActiveSessions(prefix string) []ptyhost.SessionKey {
	p.mu.Lock()
	defer p.mu.Unlock()
	active := make([]ptyhost.SessionKey, 0)
	for key, sess := range p.sessions {
		if !strings.HasPrefix(string(key), prefix) {
			continue
		}
		sess.mu.Lock()
		ended := sess.ended
		sess.mu.Unlock()
		if !ended {
			active = append(active, key)
		}
	}
	slices.Sort(active)
	return active
}

func (p *fakePTY) StopSessionsWithPrefix(_ context.Context, prefix string) {
	p.mu.Lock()
	p.stoppedPrefixes = append(p.stoppedPrefixes, prefix)
	sessions := make([]*fakePTYSession, 0)
	for key, sess := range p.sessions {
		if strings.HasPrefix(string(key), prefix) {
			sessions = append(sessions, sess)
		}
	}
	p.mu.Unlock()
	for _, sess := range sessions {
		_ = sess.att.Close()
	}
}

func (p *fakePTY) stoppedPrefixesSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.stoppedPrefixes)
}

func (p *fakePTY) LastOutput(key ptyhost.SessionKey) (time.Time, bool) {
	p.mu.Lock()
	sess, ok := p.sessions[key]
	p.mu.Unlock()
	if !ok {
		return time.Time{}, false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.last, true
}

func (p *fakePTY) Inject(_ context.Context, key ptyhost.SessionKey, actorName, actorColor, message string) error {
	run, _ := key.Run()
	p.mu.Lock()
	sess, ok := p.sessions[key]
	if ok {
		p.injects = append(p.injects, fakeInject{run: run, name: actorName, color: actorColor, message: message})
	}
	p.mu.Unlock()
	if !ok {
		return errFakeNoSession
	}
	// The banner never advances the session's last-output clock; only what
	// arrives on the attachment does. This fake has no terminal, so it
	// models none of ptyhost.Host's echo handling - that lives in the
	// ptyhost tests.
	_, err := sess.att.Stdin().Write([]byte(message + "\r"))
	return err
}

func (p *fakePTY) session(run domain.RunID) *fakePTYSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[ptyhost.RunSession(run)]
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
