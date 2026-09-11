package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/store"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

func TestCanonicalizeSource(t *testing.T) {
	public, err := CanonicalizeSource("https://Example.test/acme/repo.git", domain.MirrorAuthPublic, "")
	if err != nil || public.URL != "https://example.test/acme/repo.git" || public.Identity != "example.test/acme/repo" {
		t.Fatalf("public canonical source = %+v, %v", public, err)
	}
	github, err := CanonicalizeSource("https://github.com/acme/repo", domain.MirrorAuthDeployKey, "")
	if err != nil || github.URL != "ssh://git@github.com/acme/repo.git" || github.KnownHosts != GitHubKnownHosts {
		t.Fatalf("GitHub deploy source = %+v, %v", github, err)
	}
	generic, err := CanonicalizeSource("ssh://git.example.test/acme/repo.git", domain.MirrorAuthDeployKey, "git.example.test ssh-ed25519 AAAA")
	if err != nil || generic.URL != "ssh://git.example.test/acme/repo.git" || !generic.GenericSSH {
		t.Fatalf("generic deploy source = %+v, %v", generic, err)
	}
	for _, raw := range []string{"http://example.test/repo", "file:///tmp/repo", "ext::x", "https://user:pass@example.test/repo", "https://example.test/repo?token=x", "-c evil"} {
		if _, err := CanonicalizeSource(raw, domain.MirrorAuthPublic, ""); err == nil {
			t.Errorf("accepted forbidden public source %q", raw)
		}
	}
	if _, err := CanonicalizeSource("ssh://git.example.test/repo", domain.MirrorAuthDeployKey, ""); err == nil {
		t.Fatal("accepted generic SSH without known_hosts")
	}
}

func TestConfigureKeyModesAndStatus(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{}
	svc := newMirrorTestService(t, st, git)
	ctx := context.Background()
	public, err := svc.Configure(ctx, "public", ConfigureRequest{SourceURL: "https://example.test/acme/repo", Branch: "main", Auth: domain.MirrorAuthPublic})
	if err != nil || public.Mirror.Status != domain.MirrorStatusPending || public.PublicKey != "" {
		t.Fatalf("public configure = %+v, %v", public, err)
	}
	deploy, err := svc.Configure(ctx, "deploy", ConfigureRequest{SourceURL: "https://github.com/acme/repo", Branch: "main", Auth: domain.MirrorAuthDeployKey})
	if err != nil || deploy.PublicKey == "" || !strings.HasPrefix(deploy.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("deploy configure = %+v, %v", deploy, err)
	}
	if deploy.PublicKey == deploy.Mirror.KeyFingerprint {
		t.Fatal("fingerprint was returned as public key")
	}
	paths, err := generationPaths(svc.root, "deploy", deploy.Mirror.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if mode := mustMode(t, paths.Private); mode != 0o600 {
		t.Fatalf("private mode %o, want 600", mode)
	}
	if mode := mustMode(t, paths.Public); mode != 0o644 {
		t.Fatalf("public mode %o, want 644", mode)
	}
	status, err := svc.Status(ctx, "deploy")
	if err != nil || status.PublicKey != deploy.PublicKey {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestRefreshFailuresPersistSanitizedTypedState(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{refreshErr: &gitengine.MirrorError{Kind: gitengine.MirrorErrorAuthFailed, Cause: errors.New("secret stderr")}}
	svc := newMirrorTestService(t, st, git)
	if _, err := svc.Configure(context.Background(), "w", ConfigureRequest{SourceURL: "https://example.test/acme/repo", Branch: "main", Auth: domain.MirrorAuthPublic}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.Refresh(context.Background(), "w")
	if err == nil {
		t.Fatal("refresh unexpectedly succeeded")
	}
	var typed *gitengine.MirrorError
	if !errors.As(err, &typed) || typed.Kind != gitengine.MirrorErrorAuthFailed {
		t.Fatalf("refresh error = %T %v", err, err)
	}
	if result.Mirror.Status != domain.MirrorStatusAuthFailed {
		t.Fatalf("refresh result status = %q", result.Mirror.Status)
	}
	stored, _ := st.GetWorkspaceMirror(context.Background(), "w")
	if stored.Status != domain.MirrorStatusAuthFailed || stored.LastError != typed.Error() || strings.Contains(stored.LastError, "secret") {
		t.Fatalf("stored failure = %+v", stored)
	}
	if stored.LastAttemptAt.IsZero() {
		t.Fatal("failure did not persist attempt timestamp")
	}
}

func TestCaptureLocalAndCached(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{branchCommit: testSHA}
	svc := newMirrorTestService(t, st, git)
	local, err := svc.Capture(context.Background(), "local", "")
	if err != nil || local.Commit != testSHA || local.Source != "" || local.Configured || local.Cached || git.refreshCalls != 0 {
		t.Fatalf("local capture = %+v, %v (refreshes %d)", local, err, git.refreshCalls)
	}
	localCached, err := svc.Capture(context.Background(), "local", testSHA)
	var localCachedErr *gitengine.MirrorError
	if err == nil || !errors.As(err, &localCachedErr) || localCachedErr.Kind != gitengine.MirrorErrorInvalidRequest || localCached.Commit != "" || localCached.Branch != "" || !localCached.Cached {
		t.Fatalf("local cached capture = %+v, %v; want fail-closed invalid request", localCached, err)
	}
	if _, configureErr := svc.Configure(context.Background(), "cached", ConfigureRequest{SourceURL: "https://example.test/acme/repo", Branch: "main", Auth: domain.MirrorAuthPublic}); configureErr != nil {
		t.Fatal(configureErr)
	}
	m, _ := st.GetWorkspaceMirror(context.Background(), "cached")
	m.Status, m.AcceptedCommit = domain.MirrorStatusReady, testSHA
	_ = st.SetWorkspaceMirror(context.Background(), m)
	git.branchCommit = testSHA
	cached, err := svc.Capture(context.Background(), "cached", testSHA)
	if err != nil || cached.Commit != testSHA || cached.Source != m.SourceURL || !cached.Configured || !cached.Cached || git.refreshCalls != 0 {
		t.Fatalf("cached capture = %+v, %v (refreshes %d)", cached, err, git.refreshCalls)
	}
	m, _ = st.GetWorkspaceMirror(context.Background(), "cached")
	m.Status = domain.MirrorStatusDisabling
	if setDisablingErr := st.SetWorkspaceMirror(context.Background(), m); setDisablingErr != nil {
		t.Fatal(setDisablingErr)
	}
	disabled, err := svc.Capture(context.Background(), "cached", "")
	var disabledErr *gitengine.MirrorError
	if err == nil || !errors.As(err, &disabledErr) || disabledErr.Kind != gitengine.MirrorErrorInvalidRequest || !disabled.Configured || git.refreshCalls != 0 {
		t.Fatalf("disabling capture = %+v, %v; want fail-closed without refresh", disabled, err)
	}
}
func TestCanceledRefreshPersistsTerminalFailure(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{refreshBlock: make(chan struct{}), refreshEntered: make(chan struct{})}
	svc := newMirrorTestService(t, st, git)
	if _, err := svc.Configure(context.Background(), "w", ConfigureRequest{
		SourceURL: "https://example.test/acme/repo", Branch: "main", Auth: domain.MirrorAuthPublic,
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := svc.Refresh(ctx, "w")
		done <- err
	}()
	<-git.refreshEntered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh error = %v, want context.Canceled", err)
	}
	stored, err := st.GetWorkspaceMirror(context.Background(), "w")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status == domain.MirrorStatusRefreshing || stored.LastAttemptAt.IsZero() {
		t.Fatalf("canceled refresh left durable refreshing state: %+v", stored)
	}
}

func TestPersistFailureUsesSuppliedKind(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{}
	svc := newMirrorTestService(t, st, git)
	configured, err := svc.Configure(context.Background(), "w", ConfigureRequest{
		SourceURL: "https://github.com/acme/repo", Branch: "main", Auth: domain.MirrorAuthDeployKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := generationPaths(svc.root, "w", configured.Mirror.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if removeErr := os.Remove(paths.Private); removeErr != nil {
		t.Fatal(removeErr)
	}
	result, err := svc.Refresh(context.Background(), "w")
	var typed *gitengine.MirrorError
	if err == nil || !errors.As(err, &typed) || typed.Kind != gitengine.MirrorErrorAuthFailed {
		t.Fatalf("missing key failure = %T %v, want auth-failed MirrorError", err, err)
	}
	if result.Mirror.Status != domain.MirrorStatusAuthFailed {
		t.Fatalf("missing key status = %q", result.Mirror.Status)
	}
}

func TestCaptureFailureRetainsAcceptedCommit(t *testing.T) {
	tests := []struct {
		name          string
		cachedCommit  string
		branchCommit  string
		refreshErr    error
		refreshResult gitengine.MirrorResult
		wantCommit    string
	}{
		{name: "invalid cached commit", cachedCommit: strings.Repeat("f", 40), wantCommit: testSHA},
		{name: "cached base moved", cachedCommit: testSHA, branchCommit: strings.Repeat("a", 40), wantCommit: testSHA},
		{name: "strict refresh failure", refreshErr: &gitengine.MirrorError{Kind: gitengine.MirrorErrorOffline}, wantCommit: testSHA},
		{name: "no candidate", refreshResult: gitengine.MirrorResult{Status: domain.MirrorStatusPending}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newMirrorTestStore()
			git := &mirrorTestGit{branchCommit: tc.branchCommit, refreshErr: tc.refreshErr, refreshResult: tc.refreshResult}
			svc := newMirrorTestService(t, st, git)
			if _, err := svc.Configure(context.Background(), "w", ConfigureRequest{SourceURL: "https://example.test/acme/repo", Branch: "main", Auth: domain.MirrorAuthPublic}); err != nil {
				t.Fatal(err)
			}
			m, _ := st.GetWorkspaceMirror(context.Background(), "w")
			if tc.name != "no candidate" {
				m.AcceptedCommit = testSHA
				if err := st.SetWorkspaceMirror(context.Background(), m); err != nil {
					t.Fatal(err)
				}
			}
			got, err := svc.Capture(context.Background(), "w", tc.cachedCommit)
			if err == nil || got.Commit != tc.wantCommit {
				t.Fatalf("capture = %+v, %v; want retained commit %q", got, err, tc.wantCommit)
			}
		})
	}
}

func TestDisableClearsPolicyBeforeSecrets(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{}
	svc := newMirrorTestService(t, st, git)
	configured, err := svc.Configure(context.Background(), "w", ConfigureRequest{SourceURL: "https://github.com/acme/repo", Branch: "main", Auth: domain.MirrorAuthDeployKey})
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Disable(context.Background(), "w")
	if err != nil || result.Warning == "" {
		t.Fatalf("disable = %+v, %v", result, err)
	}
	if len(git.calls) != 2 || git.calls[0] != "configure" || git.calls[1] != "disable" {
		t.Fatalf("git calls = %v", git.calls)
	}
	if _, err := os.Stat(filepath.Join(svc.root, "w")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("secret directory remains: %v", err)
	}
	quarantineEntries, quarantineErr := os.ReadDir(filepath.Join(svc.root, ".quarantine"))
	if quarantineErr != nil && !errors.Is(quarantineErr, os.ErrNotExist) {
		t.Fatalf("inspect key quarantine after successful disable: %v", quarantineErr)
	}
	if len(quarantineEntries) != 0 {
		t.Fatalf("key quarantine retains entries after successful disable: %v", quarantineEntries)
	}
	if _, err := st.GetWorkspaceMirror(context.Background(), "w"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mirror metadata remains: %v", err)
	}
	if configured.PublicKey == "" {
		t.Fatal("configure did not return deploy public key")
	}
}

func TestDisableCallerCancellationAfterTransitionStillCompletes(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{}
	svc := newMirrorTestService(t, st, git)
	ctx := context.Background()
	configured, err := svc.Configure(ctx, "w", ConfigureRequest{
		SourceURL: "https://github.com/acme/repo", Branch: "main", Auth: domain.MirrorAuthDeployKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := generationPaths(svc.root, "w", configured.Mirror.Generation)
	if err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithCancel(ctx)
	git.disableCancel = cancel
	if _, err := svc.Disable(requestCtx, "w"); err != nil {
		t.Fatalf("disable after caller cancellation: %v", err)
	}
	if _, err := st.GetWorkspaceMirror(ctx, "w"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mirror row remains after detached lifecycle: %v", err)
	}
	if _, err := os.Stat(paths.Private); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private key remains after detached lifecycle: %v", err)
	}
}

func TestWorkspaceSerialization(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{refreshBlock: make(chan struct{}), refreshEntered: make(chan struct{})}
	svc := newMirrorTestService(t, st, git)
	if _, err := svc.Configure(context.Background(), "w", ConfigureRequest{SourceURL: "https://example.test/acme/repo", Branch: "main", Auth: domain.MirrorAuthPublic}); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { _, err := svc.Refresh(context.Background(), "w"); first <- err }()
	<-git.refreshEntered
	second := make(chan error, 1)
	go func() { _, err := svc.Refresh(context.Background(), "w"); second <- err }()
	select {
	case err := <-second:
		t.Fatalf("second refresh completed while first held lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(git.refreshBlock)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestRefreshSuccessAdoptAndRotation(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{}
	svc := newMirrorTestService(t, st, git)
	ctx := context.Background()
	first, err := svc.Configure(ctx, "w", ConfigureRequest{SourceURL: "https://github.com/acme/repo", Branch: "main", Auth: domain.MirrorAuthDeployKey})
	if err != nil {
		t.Fatal(err)
	}
	oldPaths, _ := generationPaths(svc.root, "w", first.Mirror.Generation)
	fresh, err := svc.Refresh(ctx, "w")
	if err != nil || fresh.Mirror.Status != domain.MirrorStatusReady || fresh.Mirror.AcceptedCommit != testSHA || fresh.Mirror.LastSuccessAt.IsZero() {
		t.Fatalf("successful refresh = %+v, %v", fresh, err)
	}
	adopted, err := svc.Adopt(ctx, "w", first.Mirror.Generation)
	if err != nil || adopted.Mirror.Status != domain.MirrorStatusReady || adopted.Mirror.AcceptedCommit != testSHA {
		t.Fatalf("adopt = %+v, %v", adopted, err)
	}
	second, err := svc.Configure(ctx, "w", ConfigureRequest{SourceURL: "https://github.com/acme/repo", Branch: "main", Auth: domain.MirrorAuthDeployKey})
	if err != nil || second.Mirror.Generation != first.Mirror.Generation+1 || second.PublicKey == first.PublicKey {
		t.Fatalf("rotated configure = %+v, %v", second, err)
	}
	if _, err := os.Stat(oldPaths.Private); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old private key remains: %v", err)
	}
	quarantineEntries, quarantineErr := os.ReadDir(filepath.Join(svc.root, ".quarantine"))
	if quarantineErr != nil && !errors.Is(quarantineErr, os.ErrNotExist) {
		t.Fatalf("inspect key quarantine after successful rotation: %v", quarantineErr)
	}
	if len(quarantineEntries) != 0 {
		t.Fatalf("key quarantine retains entries after successful rotation: %v", quarantineEntries)
	}
}

func TestRotationCleanupFailureIsRetryable(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{}
	svc := newMirrorTestService(t, st, git)
	ctx := context.Background()
	first, err := svc.Configure(ctx, "w", ConfigureRequest{
		SourceURL: "https://github.com/acme/repo", Branch: "main", Auth: domain.MirrorAuthDeployKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldPaths, err := generationPaths(svc.root, "w", first.Mirror.Generation)
	if err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(svc.root, ".quarantine")
	if err := os.WriteFile(quarantine, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Configure(ctx, "w", ConfigureRequest{
		SourceURL: "https://github.com/acme/repo", Branch: "main", Auth: domain.MirrorAuthDeployKey,
	}); err == nil {
		t.Fatal("rotation cleanup unexpectedly succeeded with blocked quarantine")
	}
	if _, err := os.Stat(oldPaths.Private); err != nil {
		t.Fatalf("rotation cleanup failure lost old key: %v", err)
	}
	if err := os.Remove(quarantine); err != nil {
		t.Fatal(err)
	}
	if err := retireGeneration(svc.root, "w", first.Mirror.Generation); err != nil {
		t.Fatalf("retry key retirement: %v", err)
	}
	if _, err := os.Stat(oldPaths.Private); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry did not retire old key: %v", err)
	}
}

func TestNewSweepsKeyQuarantine(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, ".quarantine", "w-1", privateKeyFile+"1")
	if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Root: root, Store: newMirrorTestStore(), Git: &mirrorTestGit{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine artifact remains after initialization sweep: %v", err)
	}
}

func TestNewReconcilesDisablingRowWithLiveKeys(t *testing.T) {
	root := t.TempDir()
	st := newMirrorTestStore()
	git := &mirrorTestGit{}
	st.mirrors["w"] = domain.WorkspaceMirror{
		WorkspaceID: "w", SourceURL: "https://example.test/source",
		SourceIdentity: "example.test/source", Branch: "main",
		Auth: domain.MirrorAuthDeployKey, Generation: 1,
		Status: domain.MirrorStatusDisabling,
	}
	paths, err := generationPaths(root, "w", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.Private, paths.Public, paths.KnownHosts} {
		if err := os.WriteFile(path, []byte("retired on startup"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(Config{Root: root, Store: st, Git: git}); err != nil {
		t.Fatal(err)
	}
	if len(git.calls) != 1 || git.calls[0] != "disable" {
		t.Fatalf("startup git calls = %v, want idempotent disable", git.calls)
	}
	if _, err := st.GetWorkspaceMirror(context.Background(), "w"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("disabling row remains after startup reconciliation: %v", err)
	}
	for _, path := range []string{paths.Private, paths.Public, paths.KnownHosts, paths.Dir} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("live startup key path remains at %s: %v", path, err)
		}
	}
}

func TestNewReconcilesDisablingRowAfterGitAlreadyGone(t *testing.T) {
	root := t.TempDir()
	st := newMirrorTestStore()
	st.mirrors["w"] = domain.WorkspaceMirror{
		WorkspaceID: "w", SourceURL: "https://example.test/source",
		SourceIdentity: "example.test/source", Branch: "main",
		Auth: domain.MirrorAuthDeployKey, Generation: 1,
		Status: domain.MirrorStatusDisabling,
	}
	if _, err := New(Config{Root: root, Store: st, Git: &mirrorTestGit{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorkspaceMirror(context.Background(), "w"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("already-disabled row remains after startup reconciliation: %v", err)
	}
}

func TestNewFailsWhenDisablingReconciliationFails(t *testing.T) {
	root := t.TempDir()
	st := newMirrorTestStore()
	st.mirrors["w"] = domain.WorkspaceMirror{
		WorkspaceID: "w", SourceURL: "https://example.test/source",
		SourceIdentity: "example.test/source", Branch: "main",
		Auth: domain.MirrorAuthPublic, Generation: 1,
		Status: domain.MirrorStatusDisabling,
	}
	disableErr := errors.New("startup disable failed")
	if svc, err := New(Config{Root: root, Store: st, Git: &mirrorTestGit{disableErr: disableErr}}); svc != nil || !errors.Is(err, disableErr) {
		t.Fatalf("startup failure = service %v, error %v; want nil service and disable error", svc, err)
	}
	stored, err := st.GetWorkspaceMirror(context.Background(), "w")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.MirrorStatusDisabling {
		t.Fatalf("failed startup recovery changed row status: %+v", stored)
	}
}

func TestGenerationSurvivesDisableAndRejectsStaleAdoption(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{}
	svc := newMirrorTestService(t, st, git)
	ctx := context.Background()

	first, err := svc.Configure(ctx, "w", ConfigureRequest{
		SourceURL: "https://example.test/source-a", Branch: "main", Auth: domain.MirrorAuthPublic,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Configure(ctx, "w", ConfigureRequest{
		SourceURL: "https://example.test/source-b", Branch: "main", Auth: domain.MirrorAuthPublic,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Mirror.Generation != 1 || second.Mirror.Generation != 2 {
		t.Fatalf("reconfigure generations = %d, %d; want 1, 2", first.Mirror.Generation, second.Mirror.Generation)
	}
	if _, disableErr := svc.Disable(ctx, "w"); disableErr != nil {
		t.Fatal(disableErr)
	}
	third, err := svc.Configure(ctx, "w", ConfigureRequest{
		SourceURL: "https://example.test/source-c", Branch: "main", Auth: domain.MirrorAuthPublic,
	})
	if err != nil {
		t.Fatal(err)
	}
	if third.Mirror.Generation != 3 {
		t.Fatalf("generation after disable = %d, want 3", third.Mirror.Generation)
	}
	beforePtr, err := st.GetWorkspaceMirror(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	before := *beforePtr
	got, err := svc.Adopt(ctx, "w", second.Mirror.Generation)
	var typed *gitengine.MirrorError
	if err == nil || !errors.As(err, &typed) || typed.Kind != gitengine.MirrorErrorInvalidRequest {
		t.Fatalf("stale generation adoption error = %v, want invalid-request MirrorError", err)
	}
	if got.Mirror != before {
		t.Fatalf("stale adoption changed returned mirror: before=%+v after=%+v", before, got.Mirror)
	}
	after, err := st.GetWorkspaceMirror(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if *after != before {
		t.Fatalf("stale adoption persisted status change: before=%+v after=%+v", before, *after)
	}
	if git.adoptCalls != 0 {
		t.Fatalf("stale adoption reached git: %d calls", git.adoptCalls)
	}
}
func TestDisableDeleteFailurePreservesState(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{}
	svc := newMirrorTestService(t, st, git)
	ctx := context.Background()
	if _, err := svc.Configure(ctx, "w", ConfigureRequest{
		SourceURL: "https://github.com/acme/repo", Branch: "main", Auth: domain.MirrorAuthDeployKey,
	}); err != nil {
		t.Fatal(err)
	}
	paths, err := generationPaths(svc.root, "w", 1)
	if err != nil {
		t.Fatal(err)
	}
	deleteErr := errors.New("delete failed")
	st.deleteErr = deleteErr
	_, err = svc.Disable(ctx, "w")
	if !errors.Is(err, deleteErr) {
		t.Fatalf("disable error = %v, want %v", err, deleteErr)
	}
	if len(git.calls) != 2 || git.calls[0] != "configure" || git.calls[1] != "disable" {
		t.Fatalf("git calls = %v, want transition followed by disable", git.calls)
	}
	stored, err := st.GetWorkspaceMirror(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.MirrorStatusDisabling {
		t.Fatalf("row changed after delete failure: %+v", stored)
	}
	if _, err := os.Stat(paths.Private); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private key remains after delete failure: %v", err)
	}
}

func TestDisableCanceledRequestStillRollsBack(t *testing.T) {
	st := newMirrorTestStore()
	git := &mirrorTestGit{disableErr: context.Canceled}
	svc := newMirrorTestService(t, st, git)
	ctx := context.Background()
	configured, err := svc.Configure(ctx, "w", ConfigureRequest{
		SourceURL: "https://github.com/acme/repo", Branch: "main", Auth: domain.MirrorAuthDeployKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := generationPaths(svc.root, "w", configured.Mirror.Generation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Disable(ctx, "w")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("disable error = %v, want context.Canceled", err)
	}
	if len(git.configureRequests) != 2 {
		t.Fatalf("rollback configure calls = %d, want 2", len(git.configureRequests))
	}
	if len(git.configureContextErrs) != 2 {
		t.Fatalf("rollback context observations = %d, want 2", len(git.configureContextErrs))
	}
	if git.configureContextErrs[1] != nil {
		t.Fatalf("rollback ran with canceled context: %v", git.configureContextErrs[1])
	}
	if _, err := st.GetWorkspaceMirror(ctx, "w"); err != nil {
		t.Fatalf("persisted mirror after canceled disable = %v", err)
	}
	if _, err := os.Stat(paths.Private); err != nil {
		t.Fatalf("private key not preserved after git failure: %v", err)
	}
}

func TestDisableRollbackFailureReportsBothErrors(t *testing.T) {
	st := newMirrorTestStore()
	rowErr := errors.New("row restore failed")
	st.setErrors = []error{nil, nil, rowErr}
	disableErr := errors.New("disable failed")
	restoreErr := errors.New("restore failed")
	git := &mirrorTestGit{disableErr: disableErr, configureErrors: []error{nil, restoreErr}}
	svc := newMirrorTestService(t, st, git)
	ctx := context.Background()
	if _, err := svc.Configure(ctx, "w", ConfigureRequest{
		SourceURL: "https://example.test/source", Branch: "main", Auth: domain.MirrorAuthPublic,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Disable(ctx, "w")
	if err == nil || !strings.Contains(err.Error(), "restore workspace mirror failed") || !strings.Contains(err.Error(), "restore previous git policy failed") {
		t.Fatalf("disable rollback error = %v, want both failures named", err)
	}
	if !errors.Is(err, disableErr) || !errors.Is(err, rowErr) || !errors.Is(err, restoreErr) {
		t.Fatalf("disable rollback error = %v, want all causes", err)
	}
}

func TestRefreshTypedFailureStatuses(t *testing.T) {
	for _, tc := range []struct {
		kind   gitengine.MirrorErrorKind
		status domain.MirrorStatus
	}{
		{gitengine.MirrorErrorOffline, domain.MirrorStatusOffline},
		{gitengine.MirrorErrorSourceMissing, domain.MirrorStatusSourceMissing},
		{gitengine.MirrorErrorRewritten, domain.MirrorStatusRewritten},
		{gitengine.MirrorErrorDiverged, domain.MirrorStatusDiverged},
		{gitengine.MirrorErrorCASConflict, domain.MirrorStatusError},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			st := newMirrorTestStore()
			git := &mirrorTestGit{refreshErr: &gitengine.MirrorError{Kind: tc.kind}}
			svc := newMirrorTestService(t, st, git)
			if _, err := svc.Configure(context.Background(), "w", ConfigureRequest{SourceURL: "https://example.test/acme/repo", Branch: "main", Auth: domain.MirrorAuthPublic}); err != nil {
				t.Fatal(err)
			}
			got, err := svc.Refresh(context.Background(), "w")
			if err == nil || got.Mirror.Status != tc.status {
				t.Fatalf("failure = %+v, %v; want %q", got, err, tc.status)
			}
		})
	}
}

type mirrorTestStore struct {
	mu        sync.Mutex
	mirrors   map[domain.WorkspaceID]domain.WorkspaceMirror
	deleteErr error
	setErrors []error
	listErr   error
}

func newMirrorTestStore() *mirrorTestStore {
	return &mirrorTestStore{mirrors: make(map[domain.WorkspaceID]domain.WorkspaceMirror)}
}
func (s *mirrorTestStore) GetWorkspaceMirror(_ context.Context, id domain.WorkspaceID) (*domain.WorkspaceMirror, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mirrors[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := m
	return &copy, nil
}
func (s *mirrorTestStore) ListWorkspaceMirrors(ctx context.Context) ([]domain.WorkspaceMirror, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	rows := make([]domain.WorkspaceMirror, 0, len(s.mirrors))
	for _, m := range s.mirrors {
		rows = append(rows, m)
	}
	return rows, nil
}
func (s *mirrorTestStore) SetWorkspaceMirror(_ context.Context, m *domain.WorkspaceMirror) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.setErrors) > 0 {
		err, rest := s.setErrors[0], s.setErrors[1:]
		s.setErrors = rest
		if err != nil {
			return err
		}
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	s.mirrors[m.WorkspaceID] = *m
	return nil
}
func (s *mirrorTestStore) DeleteWorkspaceMirror(ctx context.Context, id domain.WorkspaceID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if _, ok := s.mirrors[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.mirrors, id)
	return nil
}
func (s *mirrorTestStore) GetWorkspace(_ context.Context, id domain.WorkspaceID) (*domain.Workspace, error) {
	return &domain.Workspace{ID: id, BaseBranch: "main"}, nil
}

type mirrorTestGit struct {
	mu                   sync.Mutex
	calls                []string
	configureRequests    []gitengine.MirrorRequest
	configureContextErrs []error
	configureErrors      []error
	generations          map[domain.WorkspaceID]int64
	refreshErr           error
	refreshResult        gitengine.MirrorResult
	refreshBlock         chan struct{}
	refreshEntered       chan struct{}
	refreshCalls         int
	adoptCalls           int
	branchCommit         string
	disableErr           error
	disableCancel        context.CancelFunc
}

func (g *mirrorTestGit) MirrorGeneration(_ context.Context, ws domain.WorkspaceID) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.generations[ws], nil
}

func (g *mirrorTestGit) ConfigureWorkspaceMirror(ctx context.Context, ws domain.WorkspaceID, req gitengine.MirrorRequest) (gitengine.MirrorResult, error) {
	g.mu.Lock()
	g.calls = append(g.calls, "configure")
	g.configureRequests = append(g.configureRequests, req)
	g.configureContextErrs = append(g.configureContextErrs, ctx.Err())
	if g.generations == nil {
		g.generations = make(map[domain.WorkspaceID]int64)
	}
	err := error(nil)
	if len(g.configureErrors) > 0 {
		err, g.configureErrors = g.configureErrors[0], g.configureErrors[1:]
	}
	if err == nil && req.Generation > g.generations[ws] {
		g.generations[ws] = req.Generation
	}
	g.mu.Unlock()
	if err != nil {
		return gitengine.MirrorResult{WorkspaceID: ws}, err
	}
	return gitengine.MirrorResult{WorkspaceID: ws, CheckedAt: time.Now().UTC()}, nil
}
func (g *mirrorTestGit) RefreshWorkspaceMirror(ctx context.Context, ws domain.WorkspaceID, req gitengine.MirrorRequest) (gitengine.MirrorResult, error) {
	g.mu.Lock()
	g.calls = append(g.calls, "refresh")
	g.refreshCalls++
	entered, block, ferr, refreshResult := g.refreshEntered, g.refreshBlock, g.refreshErr, g.refreshResult
	g.mu.Unlock()
	if entered != nil {
		select {
		case <-entered:
		default:
			close(entered)
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return gitengine.MirrorResult{WorkspaceID: ws}, ctx.Err()
		}
	}
	if ferr != nil {
		return gitengine.MirrorResult{WorkspaceID: ws}, ferr
	}
	if refreshResult.Status != "" {
		refreshResult.WorkspaceID = ws
		return refreshResult, nil
	}
	return gitengine.MirrorResult{WorkspaceID: ws, Status: domain.MirrorStatusReady, ObservedCommit: testSHA, AcceptedCommit: testSHA, CheckedAt: time.Now().UTC()}, nil
}
func (g *mirrorTestGit) AdoptWorkspaceMirror(_ context.Context, ws domain.WorkspaceID, generation int64) (gitengine.MirrorResult, error) {
	g.mu.Lock()
	g.adoptCalls++
	g.mu.Unlock()
	return gitengine.MirrorResult{WorkspaceID: ws, Status: domain.MirrorStatusReady, ObservedCommit: testSHA, AcceptedCommit: testSHA}, nil
}
func (g *mirrorTestGit) DisableWorkspaceMirror(_ context.Context, _ domain.WorkspaceID) error {
	g.mu.Lock()
	g.calls = append(g.calls, "disable")
	err := g.disableErr
	g.disableErr = nil
	cancel := g.disableCancel
	g.disableCancel = nil
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return err
}
func (g *mirrorTestGit) WorkspaceBranchCommit(_ context.Context, _ domain.WorkspaceID, _ string) (string, error) {
	if g.branchCommit == "" {
		return testSHA, nil
	}
	return g.branchCommit, nil
}

func newMirrorTestService(t *testing.T, st *mirrorTestStore, git *mirrorTestGit) *Service {
	t.Helper()
	svc, err := New(Config{Root: t.TempDir(), Store: st, Git: git})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
