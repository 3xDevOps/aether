package localgw

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/overlay"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// verbStubBackend fakes the linked server for /local/v1 handler tests:
// canned Call results keyed by method plus a countable Sync dial. The
// other streaming surfaces are never reached by these handlers. Unlike
// apiStubBackend it is safe for concurrent use - the sync conflict path
// calls the backend from a background goroutine.
type verbStubBackend struct {
	apiStubBackend
	mu        sync.Mutex
	syncDials int
}

func (b *verbStubBackend) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.apiStubBackend.Call(ctx, method, params)
}

// recorded snapshots the calls made so far.
func (b *verbStubBackend) recorded() []stubCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]stubCall(nil), b.calls...)
}

func (b *verbStubBackend) Sync(string, bool) (io.ReadWriteCloser, error) {
	b.mu.Lock()
	b.syncDials++
	b.mu.Unlock()
	r, w := io.Pipe()
	return struct {
		io.Reader
		io.Writer
		io.Closer
	}{r, w, w}, nil
}

// newVerbGateway builds a gateway whose local state carries cfg.
func newVerbGateway(t *testing.T, backend Backend, cfg cli.Config) *Gateway {
	t.Helper()
	g, err := New(Config{Backend: backend, CLI: cfg})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

// useTempConfigDir points cli.Save/cli.Load at a scratch config directory.
// Both variables are needed because os.UserConfigDir reads different
// environment variables on Unix and Windows.
func useTempConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
}

func saveConfigAt(t *testing.T, cfg cli.Config, mtime time.Time) {
	t.Helper()
	if err := cli.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	path, err := cli.Path()
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("set config mtime: %v", err)
	}
}

// localGit runs one git command for the pull test's scratch repos.
func localGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestLocalTokenRequired(t *testing.T) {
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{})
	rec := do(g, http.MethodPost, "/local/v1/link.status", "{}", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeDenied {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeDenied)
	}
}

func TestLocalUnknownVerb(t *testing.T) {
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{})
	rec := do(g, http.MethodPost, "/local/v1/nope", "{}", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeMethodNotFound {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeMethodNotFound)
	}
}

func TestLocalLinkStatus(t *testing.T) {
	linked := cli.Config{
		Addr: "host:2222", User: "alice", Repo: "/src/repo", Active: "prod",
		Links: []cli.NamedLink{
			{Name: "prod", Addr: "host:2222", User: "alice"},
			{Name: "staging", Addr: "staging:2222"},
		},
	}
	g := newVerbGateway(t, &verbStubBackend{}, linked)
	rec := do(g, http.MethodPost, "/local/v1/link.status", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Linked           bool   `json:"linked"`
		ServerConfigured bool   `json:"server_configured"`
		Addr             string `json:"addr"`
		User             string `json:"user"`
		Repo             string `json:"repo"`
		Links            []struct {
			Name string `json:"name"`
			Addr string `json:"addr"`
		} `json:"links"`
		Active string `json:"active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Linked || !got.ServerConfigured || got.Addr != "host:2222" || got.User != "alice" || got.Repo != "/src/repo" {
		t.Fatalf("link.status = %+v", got)
	}
	if got.Active != "prod" {
		t.Errorf("active = %q, want prod", got.Active)
	}
	if len(got.Links) != 2 || got.Links[0].Name != "prod" || got.Links[0].Addr != "host:2222" ||
		got.Links[1].Name != "staging" || got.Links[1].Addr != "staging:2222" {
		t.Errorf("links = %+v", got.Links)
	}

	// An unlinked gateway reports linked:false rather than failing, and a
	// profile-less config omits links and active entirely.
	g = newVerbGateway(t, &verbStubBackend{}, cli.Config{})
	rec = do(g, http.MethodPost, "/local/v1/link.status", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("unlinked status = %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Linked || got.ServerConfigured {
		t.Fatalf("unlinked gateway reports configured/linked: %+v", got)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["links"]; ok {
		t.Errorf("profile-less link.status carries links: %s", rec.Body)
	}
	if _, ok := keys["active"]; ok {
		t.Errorf("top-level link.status carries active: %s", rec.Body)
	}
}
func TestLocalSnapshotRefreshesConfigAfterMtimeChange(t *testing.T) {
	useTempConfigDir(t)
	initial := cli.Config{Addr: "host:2222", User: "alice"}
	initialMtime := time.Unix(1_700_000_000, 0)
	saveConfigAt(t, initial, initialMtime)

	state := newLocalState(Config{CLI: initial})
	if got := state.snapshot(); got.Repo != "" {
		t.Fatalf("initial repo = %q, want empty", got.Repo)
	}

	updated := cli.Config{
		Addr: "host:2222",
		User: "alice",
		Repo: "/src/repo",
	}
	updatedMtime := initialMtime.Add(time.Second)
	saveConfigAt(t, updated, updatedMtime)

	if got := state.snapshot(); got.Repo != updated.Repo {
		t.Fatalf("refreshed repo = %q, want %q", got.Repo, updated.Repo)
	}
}

func TestLocalSnapshotRefreshesNamedOverlayAfterMtimeChange(t *testing.T) {
	useTempConfigDir(t)
	initial := cli.Config{
		Addr:  "default:2222",
		User:  "alice",
		Links: []cli.NamedLink{{Name: "prod", Addr: "prod:2222", Repo: "/old"}},
	}
	initialMtime := time.Unix(1_700_000_000, 0)
	saveConfigAt(t, initial, initialMtime)
	selected, ok := initial.Named("prod")
	if !ok {
		t.Fatal("initial named link missing")
	}
	state := newLocalState(Config{CLI: selected})
	if got := state.snapshot(); got.Repo != "/old" {
		t.Fatalf("initial named repo = %q, want /old", got.Repo)
	}

	updated := cli.Config{
		Addr:  "default:2222",
		User:  "alice",
		Links: []cli.NamedLink{{Name: "prod", Addr: "prod:2222", Repo: "/new"}},
	}
	saveConfigAt(t, updated, initialMtime.Add(time.Second))

	got := state.snapshot()
	if got.Repo != "/new" || got.Active != "prod" {
		t.Fatalf("refreshed named config = %+v", got)
	}
}

func TestLocalLinkRepoKeepsNewRepoForActiveNamedProfile(t *testing.T) {
	useTempConfigDir(t)
	initial := cli.Config{
		Addr:  "default:2222",
		User:  "alice",
		Links: []cli.NamedLink{{Name: "prod", Addr: "prod:2222", Repo: "/old"}},
	}
	saveConfigAt(t, initial, time.Unix(1_700_000_000, 0))
	selected, ok := initial.Named("prod")
	if !ok {
		t.Fatal("initial named link missing")
	}
	g := newVerbGateway(t, &verbStubBackend{}, selected)
	// Force the next disk write to be observable without depending on the
	// filesystem timestamp resolution.
	g.local.mtime = time.Unix(1, 0)
	repo := t.TempDir()
	localGit(t, repo, "init")

	body := `{"repo":` + strconv.Quote(repo) + `,"workspace_id":"ws_1"}`
	rec := do(g, http.MethodPost, "/local/v1/link.repo", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("link.repo = %d: %s", rec.Code, rec.Body)
	}

	got := g.local.snapshot()
	abs, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != abs {
		t.Fatalf("snapshot repo = %q, want newly linked %q", got.Repo, abs)
	}
}

func TestLocalSnapshotKeepsCachedNamedConfigWhenProfileDisappears(t *testing.T) {
	useTempConfigDir(t)
	initial := cli.Config{
		Addr:  "default:2222",
		User:  "alice",
		Repo:  "/default",
		Links: []cli.NamedLink{{Name: "prod", Addr: "prod:2222", Repo: "/prod"}},
	}
	initialMtime := time.Unix(1_700_000_000, 0)
	saveConfigAt(t, initial, initialMtime)
	selected, ok := initial.Named("prod")
	if !ok {
		t.Fatal("initial named link missing")
	}
	state := newLocalState(Config{CLI: selected})

	removed := initial
	removed.Links = nil
	removedMtime := initialMtime.Add(time.Second)
	saveConfigAt(t, removed, removedMtime)

	got := state.snapshot()
	if got.Active != "prod" || got.Addr != "prod:2222" || got.Repo != "/prod" {
		t.Fatalf("cached named config = %+v", got)
	}
	if !state.mtime.Equal(initialMtime) {
		t.Fatalf("cached mtime = %v, want unchanged %v", state.mtime, initialMtime)
	}

	restored := initial
	restored.Links = []cli.NamedLink{{Name: "prod", Addr: "prod:2222", Repo: "/restored"}}
	saveConfigAt(t, restored, removedMtime.Add(time.Second))
	if got := state.snapshot(); got.Repo != "/restored" {
		t.Fatalf("snapshot did not retry after profile restore: %+v", got)
	}
}

// link.switch never switches: the SSH identity is process-lifetime. It
// answers the restart instruction so the SPA can show it verbatim.
func TestLocalLinkSwitch(t *testing.T) {
	cfg := cli.Config{Addr: "host:2222", Links: []cli.NamedLink{{Name: "prod", Addr: "host:2222"}}}
	g := newVerbGateway(t, &verbStubBackend{}, cfg)

	rec := do(g, http.MethodPost, "/local/v1/link.switch", `{"name":"prod"}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Errorf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if perr.Message != "restart aether gui --server prod to switch servers" {
		t.Errorf("message = %q", perr.Message)
	}

	// A missing name is a params error, not the instruction.
	rec = do(g, http.MethodPost, "/local/v1/link.switch", "{}", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-name status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeInvalidParams {
		t.Errorf("no-name code = %d, want %d", perr.Code, protocol.CodeInvalidParams)
	}
}

func TestLocalPull(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("test ssh shim is a POSIX shell script")
	}
	// A "remote" repo with one commit on the run branch, and a local
	// linked repo to pull into. The remote directory carries a .git
	// suffix so the ssh URL's path resolves to it verbatim.
	remote := filepath.Join(t.TempDir(), "ws_1.git")
	if err := os.Mkdir(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	localGit(t, remote, "init", "-b", "aether/run_1")
	localGit(t, remote, "commit", "--allow-empty", "-m", "run work")
	local := t.TempDir()
	localGit(t, local, "init", "-b", "main")
	localGit(t, local, "remote", "add", "aether", remote)

	// GitURL renders ssh://alice@host:2222/<ws>.git; a GIT_SSH_COMMAND
	// shim executes the wrapped git-upload-pack locally instead of
	// dialing, so the fetch really moves objects.
	shim := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nfor last; do :; done\neval \"$last\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", shim)
	// An unknown GIT_SSH_COMMAND defaults to the "simple" variant, which
	// refuses the URL's port; declare the OpenSSH argv convention.
	t.Setenv("GIT_SSH_VARIANT", "ssh")

	coords, err := json.Marshal(protocol.RunPullResult{
		WorkspaceID: strings.TrimSuffix(strings.TrimPrefix(remote, "/"), ".git"),
		Branch:      "aether/run_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodRunPull: coords},
	}}
	g := newVerbGateway(t, backend, cli.Config{Addr: "host:2222", User: "alice", Repo: local})

	rec := do(g, http.MethodPost, `/local/v1/pull`, `{"run_id":"run_1"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Branch  string `json:"branch"`
		Ref     string `json:"ref"`
		Output  string `json:"output"`
		Current bool   `json:"current"`
		Dirty   bool   `json:"dirty"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Branch != "aether/run_1" || got.Ref != "refs/remotes/aether/aether/run_1" {
		t.Fatalf("pull = %+v", got)
	}
	if got.Current || got.Dirty {
		t.Fatalf("pull state = current %v dirty %v, want false false", got.Current, got.Dirty)
	}
	want := localGit(t, remote, "rev-parse", "aether/run_1")
	if fetched := localGit(t, local, "rev-parse", "refs/remotes/aether/aether/run_1"); fetched != want {
		t.Fatalf("tracking ref = %s, want %s", fetched, want)
	}
	if calls := backend.recorded(); len(calls) != 1 || calls[0].method != protocol.MethodRunPull {
		t.Fatalf("backend calls = %+v", calls)
	} else if want := `{"run_id":"run_1"}`; calls[0].params != want {
		t.Fatalf("run.pull params = %s", calls[0].params)
	}
}

func TestLocalPullRequiresLinkedRepo(t *testing.T) {
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{Addr: "host:2222"})
	rec := do(g, http.MethodPost, "/local/v1/pull", `{"run_id":"run_1"}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, "no linked repo") {
		t.Fatalf("message = %q", perr.Message)
	}
}

func TestLocalPullRequiresRunID(t *testing.T) {
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{Addr: "host:2222", Repo: "/src"})
	rec := do(g, http.MethodPost, "/local/v1/pull", `{}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidParams)
	}
}

// stubOverlaySession fakes the mutagen engine for the sync verb tests.
type stubOverlaySession struct {
	runErr chan error
}

func (s *stubOverlaySession) Start(context.Context, string) error { return nil }
func (s *stubOverlaySession) Run(ctx context.Context) error {
	select {
	case err := <-s.runErr:
		return err
	case <-ctx.Done():
		return nil
	}
}
func (s *stubOverlaySession) Close() {}

func TestLocalSyncLifecycle(t *testing.T) {
	backend := &verbStubBackend{}
	g := newVerbGateway(t, backend, cli.Config{Addr: "host:2222", Repo: t.TempDir()})
	sess := &stubOverlaySession{runErr: make(chan error, 1)}
	g.local.sync.NewSession = func(_ string, dial overlay.Dialer) (localops.OverlaySession, error) {
		// Exercise the dial path so Backend.Sync is genuinely wired.
		stream, err := dial(context.Background())
		if err != nil {
			return nil, err
		}
		_ = stream.Close()
		return sess, nil
	}

	rec := do(g, http.MethodPost, "/local/v1/sync.start", `{"run_id":"run_1","force":true}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync.start = %d: %s", rec.Code, rec.Body)
	}
	var started struct {
		RunID string `json:"run_id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.RunID != "run_1" || started.State != "running" {
		t.Fatalf("sync.start = %+v", started)
	}
	backend.mu.Lock()
	dials := backend.syncDials
	backend.mu.Unlock()
	if dials != 1 {
		t.Fatalf("sync dials = %d", dials)
	}

	rec = do(g, http.MethodPost, "/local/v1/sync.status", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync.status = %d: %s", rec.Code, rec.Body)
	}
	var status struct {
		Sessions []struct {
			RunID    string  `json:"run_id"`
			State    string  `json:"state"`
			Conflict *string `json:"conflict"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Sessions) != 1 || status.Sessions[0].RunID != "run_1" ||
		status.Sessions[0].State != "running" || status.Sessions[0].Conflict != nil {
		t.Fatalf("sync.status = %+v", status)
	}

	rec = do(g, http.MethodPost, "/local/v1/sync.stop", `{"run_id":"run_1"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync.stop = %d: %s", rec.Code, rec.Body)
	}
	var stopped struct {
		RunID string `json:"run_id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.RunID != "run_1" || stopped.State != "stopped" {
		t.Fatalf("sync.stop = %+v", stopped)
	}

	rec = do(g, http.MethodPost, "/local/v1/sync.stop", `{"run_id":"run_9"}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("sync.stop unknown run = %d", rec.Code)
	}
}

func TestLocalSyncConflictReportsToServer(t *testing.T) {
	backend := &verbStubBackend{}
	g := newVerbGateway(t, backend, cli.Config{Addr: "host:2222", Repo: t.TempDir()})
	sess := &stubOverlaySession{runErr: make(chan error, 1)}
	g.local.sync.NewSession = func(string, overlay.Dialer) (localops.OverlaySession, error) {
		return sess, nil
	}

	rec := do(g, http.MethodPost, "/local/v1/sync.start", `{"run_id":"run_1"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync.start = %d: %s", rec.Code, rec.Body)
	}
	sess.runErr <- &overlay.Conflict{SessionID: "sync_1", Files: []string{"f.txt"}}

	deadline := time.After(2 * time.Second)
	for {
		if calls := backend.recorded(); len(calls) == 1 {
			if calls[0].method != protocol.MethodSyncConflict {
				t.Fatalf("conflict call method = %s", calls[0].method)
			}
			var params protocol.SyncConflictParams
			if err := json.Unmarshal([]byte(calls[0].params), &params); err != nil {
				t.Fatal(err)
			}
			if params.RunID != "run_1" || params.SyncSessionID != "sync_1" ||
				len(params.Files) != 1 || params.Files[0] != "f.txt" {
				t.Fatalf("conflict params = %+v", params)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("sync.conflict never reported; calls = %+v", backend.recorded())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestLocalSyncStartRequiresLinkedRepo(t *testing.T) {
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{Addr: "host:2222"})
	rec := do(g, http.MethodPost, "/local/v1/sync.start", `{"run_id":"run_1"}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
}

func TestLocalImageScaffold(t *testing.T) {
	// The repo path goes through json.Marshal rather than string
	// concatenation: a Windows path's backslashes are escape characters
	// inside a JSON string literal.
	scaffoldBody := func(repo, kind string) string {
		t.Helper()
		raw, err := json.Marshal(struct {
			Repo string `json:"repo"`
			Kind string `json:"kind"`
		}{Repo: repo, Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	repo := t.TempDir()
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{Addr: "host:2222"})
	rec := do(g, http.MethodPost, "/local/v1/image.scaffold",
		scaffoldBody(repo, "devcontainer"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("image.scaffold = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Written []string `json:"written"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Written) != 3 {
		t.Fatalf("written = %v", got.Written)
	}
	if got.Written[2] != filepath.Join(repo, ".devcontainer", "devcontainer.json") {
		t.Fatalf("written[2] = %q", got.Written[2])
	}

	// Overwrite refusal is an invalid-state, not an internal error.
	rec = do(g, http.MethodPost, "/local/v1/image.scaffold",
		scaffoldBody(repo, "dockerfile"), true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second scaffold = %d: %s", rec.Code, rec.Body)
	}
	if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d", perr.Code)
	}

	rec = do(g, http.MethodPost, "/local/v1/image.scaffold",
		scaffoldBody(t.TempDir(), "vm"), true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad kind = %d", rec.Code)
	}
}

// sshShim makes ssh:// git URLs resolve to local paths, so a test push
// really moves objects without dialing anything. Same trick as the pull
// test: the shim runs the wrapped git-receive-pack itself.
func sshShim(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test ssh shim is a POSIX shell script")
	}
	shim := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nfor last; do :; done\neval \"$last\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", shim)
	// An unknown GIT_SSH_COMMAND defaults to the "simple" variant, which
	// refuses the URL's port; declare the OpenSSH argv convention.
	t.Setenv("GIT_SSH_VARIANT", "ssh")
}

// pushGateway wires a gateway whose linked repo holds one commit on
// branch and an `aether` remote pointing, through the ssh shim, at a
// bare repo standing in for the workspace. The server reports that one
// workspace with that base branch. It returns the gateway, the bare
// remote's path, and the workspace ID the remote URL carries.
func pushGateway(t *testing.T, branch string) (*Gateway, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	sshShim(t)

	// The bare directory carries a .git suffix so the ssh URL's path
	// resolves to it verbatim.
	remote := filepath.Join(t.TempDir(), "wsp_1.git")
	localGit(t, t.TempDir(), "init", "--bare", "-b", branch, remote)
	wsID := strings.TrimSuffix(strings.TrimPrefix(remote, "/"), ".git")

	local := t.TempDir()
	localGit(t, local, "init", "-b", branch)
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	localGit(t, local, "add", "README.md")
	localGit(t, local, "commit", "-m", "seed")
	localGit(t, local, "remote", "add", "aether", cli.GitURL("alice", "host:2222", wsID))

	list, err := json.Marshal(protocol.WorkspaceListResult{
		Workspaces: []protocol.Workspace{{ID: wsID, Name: "myproject", BaseBranch: branch}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodWorkspaceList: list},
	}}
	g := newVerbGateway(t, backend, cli.Config{Addr: "host:2222", User: "alice", Repo: local})
	return g, remote, wsID
}

// pushBody is one repo.push request naming a workspace.
func pushBody(t *testing.T, wsID string) string {
	t.Helper()
	body, err := json.Marshal(struct {
		WorkspaceID string `json:"workspace_id"`
	}{wsID})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// syncGateway extends the push fixture with an origin remote whose branch is
// ahead of the linked checkout.
func syncGateway(t *testing.T, branch string) (*Gateway, string, string) {
	t.Helper()
	g, aether, wsID := pushGateway(t, branch)
	local := g.local.snapshot().Repo
	origin := filepath.Join(t.TempDir(), "origin.git")
	localGit(t, t.TempDir(), "init", "--bare", "-b", branch, origin)
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	localGit(t, local, "remote", "add", "origin", origin)
	localGit(t, local, "push", "origin", refspec)

	clone := filepath.Join(t.TempDir(), "origin-clone")
	localGit(t, t.TempDir(), "clone", "--branch", branch, origin, clone)
	if err := os.WriteFile(filepath.Join(clone, "origin.txt"), []byte("from origin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	localGit(t, clone, "add", "origin.txt")
	localGit(t, clone, "commit", "-m", "origin update")
	localGit(t, clone, "push", "origin", refspec)
	return g, aether, wsID
}

// The seeding push runs the user's base branch, not a hardcoded main.
func TestLocalRepoPush(t *testing.T) {
	g, remote, wsID := pushGateway(t, "trunk")

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, wsID), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("repo.push = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Branch string `json:"branch"`
		Remote string `json:"remote"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Branch != "trunk" || got.Remote != "aether" {
		t.Fatalf("repo.push = %+v", got)
	}
	if !strings.Contains(got.Output, "trunk") {
		t.Fatalf("output does not mention the branch: %q", got.Output)
	}
	if localGit(t, remote, "rev-parse", "trunk") == "" {
		t.Fatal("remote has no trunk")
	}
}

func TestLocalRepoSync(t *testing.T) {
	g, remote, wsID := syncGateway(t, "trunk")
	local := g.local.snapshot().Repo
	beforeHead := localGit(t, local, "rev-parse", "HEAD")

	rec := do(g, http.MethodPost, "/local/v1/repo.sync", pushBody(t, wsID), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("repo.sync = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Branch string `json:"branch"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Branch != "trunk" {
		t.Fatalf("repo.sync = %+v", got)
	}
	if !strings.Contains(got.Output, "trunk") {
		t.Fatalf("output does not mention the branch: %q", got.Output)
	}
	if got := localGit(t, remote, "rev-parse", "trunk"); got == beforeHead {
		t.Fatal("repo.sync did not advance the aether branch")
	}
	if got := localGit(t, local, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("repo.sync changed local HEAD from %s to %s", beforeHead, got)
	}
}

func TestLocalRepoSyncRequiresLinkedRepo(t *testing.T) {
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{Addr: "host:2222"})
	rec := do(g, http.MethodPost, "/local/v1/repo.sync", `{}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, "no linked repo") {
		t.Fatalf("error = %+v", perr)
	}
}

// A rejected push is the server's word, not the gateway's; the handler
// answers with git's own text so branch protection reads as itself.
func TestLocalRepoPushSurfacesGitRefusal(t *testing.T) {
	g, remote, wsID := pushGateway(t, "main")
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'main is protected' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, wsID), true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if perr := decodeError(t, rec.Body.Bytes()); !strings.Contains(perr.Message, "main is protected") {
		t.Fatalf("message = %q", perr.Message)
	}
}

// A repository the user has not committed in yet is theirs to fix, so it
// answers invalid state with the next step rather than a git failure.
func TestLocalRepoPushRefusesAnEmptyRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	local := t.TempDir()
	localGit(t, local, "init", "-b", "main")
	list, err := json.Marshal(protocol.WorkspaceListResult{
		Workspaces: []protocol.Workspace{{ID: "wsp_1", Name: "myproject", BaseBranch: "main"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodWorkspaceList: list},
	}}
	g := newVerbGateway(t, backend, cli.Config{Addr: "host:2222", User: "alice", Repo: local})

	rec := do(g, http.MethodPost, "/local/v1/repo.push", `{}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, "no commits yet") {
		t.Fatalf("error = %+v", perr)
	}
}

func TestLocalRepoPushRequiresLinkedRepo(t *testing.T) {
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{Addr: "host:2222"})
	rec := do(g, http.MethodPost, "/local/v1/repo.push", `{}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, "no linked repo") {
		t.Fatalf("error = %+v", perr)
	}
}

// The base branch comes from the named workspace, the push lands wherever
// the `aether` remote points. When those are two different workspaces the
// verb refuses instead of reporting a seed it did not perform.
func TestLocalRepoPushRefusesAWorkspaceTheRemoteDoesNotServe(t *testing.T) {
	g, remote, wsID := pushGateway(t, "main")
	// link.repo has since re-pointed the remote at another workspace.
	other := cli.GitURL("alice", "host:2222", "wsp_other")
	localGit(t, g.local.snapshot().Repo, "remote", "set-url", "aether", other)

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, wsID), true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState || !strings.Contains(perr.Message, other) {
		t.Fatalf("error = %+v", perr)
	}
	if refs := localGit(t, remote, "for-each-ref"); refs != "" {
		t.Fatalf("the refused push still wrote refs: %s", refs)
	}
}

// The workspace check reads the repository before the push does, so a
// linked folder the user has since moved must still answer with the
// preflight's own words, not a bare git exit status from the check.
func TestLocalRepoPushRefusesALinkedFolderThatIsNotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	gone := t.TempDir()
	list, err := json.Marshal(protocol.WorkspaceListResult{
		Workspaces: []protocol.Workspace{{ID: "wsp_1", Name: "myproject", BaseBranch: "main"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodWorkspaceList: list},
	}}
	g := newVerbGateway(t, backend, cli.Config{Addr: "host:2222", User: "alice", Repo: gone})

	rec := do(g, http.MethodPost, "/local/v1/repo.push", pushBody(t, "wsp_1"), true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, gone) || !strings.Contains(perr.Message, "not a git repository") {
		t.Fatalf("message = %q", perr.Message)
	}
}
