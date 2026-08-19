package gitengine

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// The scheduler's and sshd's consumer-side seam interfaces, copied verbatim
// from the Wave 1 contract (§4): *Engine must satisfy both exactly.
type schedulerGitEngine interface {
	CreateRunCheckout(ctx context.Context, ws domain.WorkspaceID, run domain.RunID, baseBranch, task string) (checkoutPath, branch string, err error)
	CommitAll(ctx context.Context, run domain.RunID, message string) (commit string, err error)
	PublishRunBranch(ctx context.Context, run domain.RunID) (commit string, err error)
	RemoveRunCheckout(ctx context.Context, run domain.RunID) error
	StartDiffWatch(ctx context.Context, session domain.SessionID, run domain.RunID) error
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
	if _, _, err := e.CreateRunCheckout(ctx, "nope", "r1", "main", "task"); !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("CreateRunCheckout missing repo: %v, want ErrRepoNotFound", err)
	}
	if _, err := e.CommitAll(ctx, "r1", "msg"); !errors.Is(err, ErrCheckoutNotFound) {
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
