package gitengine

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// The scheduler's and sshd's consumer-side seam interfaces, copied verbatim
// from the Wave 1 contract (§4): *Engine must satisfy both exactly.
type schedulerGitEngine interface {
	CreateRunCheckout(ctx context.Context, ws domain.WorkspaceID, run domain.RunID, baseBranch, task, origin string) (checkoutPath, branch string, err error)
	CommitAll(ctx context.Context, run domain.RunID, message string, author domain.GitIdentity, signingKey []byte) (commit string, err error)
	PublishRunBranch(ctx context.Context, run domain.RunID) (commit string, err error)
	RemoveRunCheckout(ctx context.Context, run domain.RunID) error
	StartDiffWatch(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) error
	StopDiffWatch(run domain.RunID)
	LastFileChange(run domain.RunID) (time.Time, bool)
}

type sshdGitTransport interface {
	UploadPack(ctx context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error)
	ReceivePack(ctx context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error)
}

var (
	_ schedulerGitEngine = (*Engine)(nil)
	_ sshdGitTransport   = (*Engine)(nil)
)

func newUnitEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	e, err := New(Config{ReposDir: filepath.Join(dir, "repos"), CheckoutsDir: filepath.Join(dir, "checkouts")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestNewValidatesAndDefaults(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New with empty dirs should fail")
	}
	e := newUnitEngine(t)
	if e.cfg.GitPath != "git" {
		t.Errorf("GitPath default = %q", e.cfg.GitPath)
	}
	if e.cfg.QuietPeriod != 2*time.Second || e.cfg.MinInterval != 10*time.Second || e.cfg.MaxInterval != 60*time.Second {
		t.Errorf("interval defaults = %v %v %v", e.cfg.QuietPeriod, e.cfg.MinInterval, e.cfg.MaxInterval)
	}
}

func TestValidateID(t *testing.T) {
	valid := []string{"0b9x7k2m4q6s8v0w2y4z6a8c9e", "ws1", "a", "A-b_c.d", "run-42"}
	for _, id := range valid {
		if err := validateID(id); err != nil {
			t.Errorf("validateID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{
		"", ".", "..", "a/b", "a\\b", "../etc", "a/../b", "a..b",
		".hidden", "-flag", "a b", "a\x00b", "å", strings.Repeat("a", 129),
	}
	for _, id := range invalid {
		if err := validateID(id); err == nil {
			t.Errorf("validateID(%q) = nil, want error", id)
		}
	}
}

func TestPathTraversalRejected(t *testing.T) {
	e := newUnitEngine(t)
	ctx := t.Context()
	for _, ws := range []domain.WorkspaceID{"../outside", "a/b", "..", ""} {
		if _, err := e.InitWorkspaceRepo(ctx, ws); err == nil {
			t.Errorf("InitWorkspaceRepo(%q) accepted a traversal id", ws)
		}
		if code, err := e.UploadPack(ctx, ws, strings.NewReader(""), io.Discard, io.Discard); err == nil || code != -1 {
			t.Errorf("UploadPack(%q) = (%d, %v), want error", ws, code, err)
		}
	}
	for _, run := range []domain.RunID{"../outside", "x/y", ".."} {
		if err := e.RemoveRunCheckout(ctx, run); err == nil {
			t.Errorf("RemoveRunCheckout(%q) accepted a traversal id", run)
		}
	}
}

func TestMissingRepoAndCheckoutErrors(t *testing.T) {
	e := newUnitEngine(t)
	ctx := t.Context()
	if _, err := e.UploadPack(ctx, "nope", strings.NewReader(""), io.Discard, io.Discard); !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("UploadPack missing repo: %v, want ErrRepoNotFound", err)
	}
	if _, err := e.ReceivePack(ctx, "nope", strings.NewReader(""), io.Discard, io.Discard); !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("ReceivePack missing repo: %v, want ErrRepoNotFound", err)
	}
	if _, _, err := e.CreateRunCheckout(ctx, "nope", "r1", "main", "task", ""); !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("CreateRunCheckout missing repo: %v, want ErrRepoNotFound", err)
	}
	if _, err := e.CommitAll(ctx, "r1", "msg", domain.GitIdentity{}, nil); !errors.Is(err, ErrCheckoutNotFound) {
		t.Errorf("CommitAll missing checkout: %v, want ErrCheckoutNotFound", err)
	}
	if _, err := e.PublishRunBranch(ctx, "r1"); !errors.Is(err, ErrCheckoutNotFound) {
		t.Errorf("PublishRunBranch missing checkout: %v, want ErrCheckoutNotFound", err)
	}
	if err := e.StartDiffWatch(ctx, "s1", "r1"); !errors.Is(err, ErrCheckoutNotFound) {
		t.Errorf("StartDiffWatch missing checkout: %v, want ErrCheckoutNotFound", err)
	}
	if err := e.RemoveRunCheckout(ctx, "r1"); err != nil {
		t.Errorf("RemoveRunCheckout missing checkout should be idempotent: %v", err)
	}
	if _, ok := e.LastFileChange("r1"); ok {
		t.Error("LastFileChange without a watch should report false")
	}
	e.StopDiffWatch("r1") // idempotent no-op
}

func TestPublishBranchUsesRunMetadataWithoutRegistry(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newUnitEngine(t)
	e.cfg.Bus = bus

	const (
		run       domain.RunID       = "r1"
		branch    string             = "aether/r1"
		workspace domain.WorkspaceID = "ws1"
		commit                       = "0123456789abcdef"
	)
	if metaErr := e.writeRunMeta(run, runMeta{Base: "main", Branch: branch, Workspace: workspace}); metaErr != nil {
		t.Fatal(metaErr)
	}
	sub, err := bus.Subscribe(t.Context(), events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeGitBranch}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	var (
		callbackRun    domain.RunID
		callbackCommit string
	)
	e.cfg.OnBranchPublished = func(gotRun domain.RunID, gotCommit string, _ time.Time) {
		callbackRun, callbackCommit = gotRun, gotCommit
	}
	e.publishBranch(t.Context(), run, commit)

	select {
	case event := <-sub.Events():
		payload, ok := event.Payload.(events.GitBranchPayload)
		if !ok {
			t.Fatalf("payload type = %T, want events.GitBranchPayload", event.Payload)
		}
		if event.WorkspaceID != workspace || event.RunID != run {
			t.Fatalf("event scope = %s/%s, want %s/%s", event.WorkspaceID, event.RunID, workspace, run)
		}
		if payload.WorkspaceID != workspace || payload.Branch != branch || payload.Commit != commit {
			t.Fatalf("event payload = %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("publishBranch did not emit git.branch")
	}
	if callbackRun != run || callbackCommit != commit {
		t.Fatalf("callback = %s/%s, want %s/%s", callbackRun, callbackCommit, run, commit)
	}
}

func TestMirrorValidationRequiresExplicitTestSeamForLocalSources(t *testing.T) {
	req := MirrorRequest{
		SourceURL:  "/tmp/upstream.git",
		Branch:     "main",
		Generation: 1,
		Auth:       domain.MirrorAuthPublic,
	}
	if err := validateMirrorRequest(req, false); err == nil {
		t.Fatal("local-file mirror source accepted without the test seam")
	}
	if err := validateMirrorRequest(req, true); err != nil {
		t.Fatalf("local-file source rejected through explicit seam: %v", err)
	}
}

func TestMirrorResolutionRejectsUnsafeAddresses(t *testing.T) {
	e := newUnitEngine(t)
	unsafe := []string{
		"127.0.0.1", "10.0.0.1", "169.254.1.1", "224.0.0.1",
		"0.0.0.0", "100.64.0.1", "::1", "fc00::1", "fe80::1",
		"ff02::1", "::",
	}
	for _, raw := range unsafe {
		raw := raw
		e.cfg.MirrorResolve = func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP(raw)}, nil
		}
		if _, err := e.resolveMirrorHost(t.Context(), "public.example"); err == nil {
			t.Errorf("resolveMirrorHost(%q) accepted unsafe address", raw)
		}
	}
	e.cfg.MirrorResolve = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("192.168.1.1")}, nil
	}
	if _, err := e.resolveMirrorHost(t.Context(), "public.example"); err == nil {
		t.Fatal("resolveMirrorHost accepted a mixed safe and unsafe result")
	}
	e.cfg.MirrorResolve = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("2001:4860:4860::8888")}, nil
	}
	if got, err := e.resolveMirrorHost(t.Context(), "public.example"); err != nil || len(got) != 2 {
		t.Fatalf("resolveMirrorHost safe result = %v, %v", got, err)
	}
}

func TestMirrorCurloptResolveFormatting(t *testing.T) {
	if got := mirrorCurloptResolve("github.example", net.ParseIP("203.0.113.9")); got != "github.example:443:203.0.113.9" {
		t.Fatalf("IPv4 curloptResolve = %q", got)
	}
	if got := mirrorCurloptResolve("github.example", net.ParseIP("2001:4860:4860::8888")); got != "github.example:443:[2001:4860:4860::8888]" {
		t.Fatalf("IPv6 curloptResolve = %q", got)
	}
}

func TestMirrorFetchPublicHTTPSArguments(t *testing.T) {
	e := newUnitEngine(t)
	record := filepath.Join(t.TempDir(), "args")
	git := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuoteMirror(record) + "\n"
	if err := os.WriteFile(git, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e.cfg.GitPath = git
	e.cfg.MirrorResolve = func(_ context.Context, host string) ([]net.IP, error) {
		if host != "github.example" {
			t.Fatalf("resolver host = %q", host)
		}
		return []net.IP{net.ParseIP("203.0.113.9"), net.ParseIP("2001:4860:4860::8888")}, nil
	}
	repo := t.TempDir()
	req := MirrorRequest{
		SourceURL: "https://github.example/aether.git",
		Branch:    "main",
		Auth:      domain.MirrorAuthPublic,
	}
	if err := e.fetchMirror(t.Context(), repo, req, "refs/aether/incoming/test"); err != nil {
		t.Fatalf("fetchMirror: %v", err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{
		"-C", repo, "-c", "safe.directory=*",
		"-c", "http.followRedirects=false",
		"-c", "http.curloptResolve=github.example:443:203.0.113.9",
		"-c", "http.curloptResolve=github.example:443:[2001:4860:4860::8888]",
		"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
		"https://github.example/aether.git", "+refs/heads/main:refs/aether/incoming/test",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("git args = %#v, want %#v", args, want)
	}
}

func TestMirrorRequestRejectsHTTPSPortsAndSSHPasswords(t *testing.T) {
	if err := validateMirrorRequest(MirrorRequest{
		SourceURL: "https://github.example:8443/aether.git",
		Branch:    "main",
		Auth:      domain.MirrorAuthPublic,
	}, false); err == nil {
		t.Fatal("public HTTPS non-443 port accepted")
	}
	req := MirrorRequest{
		SourceURL:      "ssh://deploy:secret@github.example/aether.git",
		Branch:         "main",
		Auth:           domain.MirrorAuthDeployKey,
		PrivateKeyPath: "/srv/key",
		KnownHostsPath: "/srv/known_hosts",
	}
	if err := validateMirrorRequest(req, false); err == nil {
		t.Fatal("deploy-key SSH password accepted")
	}
	req.SourceURL = "ssh://deploy@github.example/aether.git"
	if err := validateMirrorRequest(req, false); err != nil {
		t.Fatalf("deploy-key SSH username rejected: %v", err)
	}
}

func TestConfigureWorkspaceRepoHidesAetherRefsForBothPackServices(t *testing.T) {
	e := newUnitEngine(t)
	repo, err := e.InitWorkspaceRepo(t.Context(), "hidden")
	if err != nil {
		t.Fatal(err)
	}
	if _, initErr := e.InitWorkspaceRepo(t.Context(), "hidden"); initErr != nil {
		t.Fatal(initErr)
	}
	got, err := e.git(t.Context(), repo, "config", "--get-all", "transfer.hideRefs")
	if err != nil || strings.TrimSpace(got) != "refs/aether" {
		t.Fatalf("transfer.hideRefs = %q, %v", got, err)
	}
}

func TestMirrorFetchDeployKeySkipsDNSResolver(t *testing.T) {
	e := newUnitEngine(t)
	git := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.cfg.GitPath = git
	e.cfg.MirrorResolve = func(context.Context, string) ([]net.IP, error) {
		t.Fatal("deploy-key fetch invoked DNS resolver")
		return nil, nil
	}
	req := MirrorRequest{
		SourceURL:      "ssh://deploy@github.example/aether.git",
		Branch:         "main",
		Auth:           domain.MirrorAuthDeployKey,
		PrivateKeyPath: "/srv/key",
		KnownHostsPath: "/srv/known_hosts",
	}
	if err := e.fetchMirror(t.Context(), t.TempDir(), req, "refs/aether/incoming/test"); err != nil {
		t.Fatalf("deploy-key fetch: %v", err)
	}
}

func TestDisableWorkspaceMirrorOrdersPolicyBeforeAtomicDelete(t *testing.T) {
	e := newUnitEngine(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := e.InitWorkspaceRepo(t.Context(), "disable-order")
	if err != nil {
		t.Fatal(err)
	}
	hashCmd := exec.CommandContext(t.Context(), realGit, "-C", repo, "-c", "safe.directory=*", "hash-object", "-w", "--stdin")
	hashCmd.Stdin = strings.NewReader("disable-order")
	hashOutput, err := hashCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(hashOutput))
	if _, updateAcceptedErr := e.git(t.Context(), repo, "update-ref", "refs/aether/mirror/7/accepted", commit); updateAcceptedErr != nil {
		t.Fatal(updateAcceptedErr)
	}
	if _, updateCandidateErr := e.git(t.Context(), repo, "update-ref", "refs/aether/mirror/7/candidate", commit); updateCandidateErr != nil {
		t.Fatal(updateCandidateErr)
	}
	for key, value := range map[string]string{
		mirrorActiveGenerationConfig: "7",
		mirrorBaseConfig:             "refs/heads/main",
	} {
		if _, configErr := e.git(t.Context(), repo, "config", key, value); configErr != nil {
			t.Fatal(configErr)
		}
	}
	logPath := filepath.Join(t.TempDir(), "git.log")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuoteMirror(logPath) + "\n" +
		"case \"$*\" in *'update-ref --stdin'*) exit 42;; esac\n" +
		"exec " + shellQuoteMirror(realGit) + " \"$@\"\n"
	if writeWrapperErr := os.WriteFile(wrapper, []byte(script), 0o755); writeWrapperErr != nil {
		t.Fatal(writeWrapperErr)
	}
	e.cfg.GitPath = wrapper
	if disableErr := e.DisableWorkspaceMirror(t.Context(), "disable-order"); disableErr == nil {
		t.Fatal("DisableWorkspaceMirror unexpectedly succeeded with failing ref transaction")
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logLines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(logLines) == 0 || !strings.Contains(logLines[len(logLines)-1], "update-ref --stdin") {
		t.Fatalf("last disable command = %q, want atomic ref deletion", logLines)
	}
	activeUnset, baseUnset := -1, -1
	for i, line := range logLines {
		if strings.Contains(line, "config --unset-all "+mirrorActiveGenerationConfig) {
			activeUnset = i
		}
		if strings.Contains(line, "config --unset-all "+mirrorBaseConfig) {
			baseUnset = i
		}
	}
	if activeUnset < 0 || baseUnset < 0 {
		t.Fatalf("disable log lacks policy removal: %v", logLines)
	}
	if activeUnset > baseUnset {
		t.Fatalf("active policy removed after base policy: %v", logLines)
	}
	if got := readRefBestEffort(t.Context(), e, repo, "refs/aether/mirror/7/accepted"); got == "" {
		t.Fatal("failed disable lost accepted ref")
	}
	if got := readRefBestEffort(t.Context(), e, repo, "refs/aether/mirror/7/candidate"); got == "" {
		t.Fatal("failed disable lost candidate ref")
	}

	e.cfg.GitPath = realGit
	e.cfg.MirrorFetch = func(context.Context, string, MirrorRequest, string) error { return nil }
	if _, err := e.ConfigureWorkspaceMirror(t.Context(), "disable-order", MirrorRequest{
		SourceURL:  "/tmp/test-source.git",
		Branch:     "main",
		Generation: 7,
		Auth:       domain.MirrorAuthPublic,
	}); err != nil {
		t.Fatalf("restore mirror policy: %v", err)
	}
}
