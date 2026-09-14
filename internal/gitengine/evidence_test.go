package gitengine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func seedEvidenceWorkspace(t *testing.T, e *Engine, ws domain.WorkspaceID) {
	t.Helper()
	ctx := t.Context()
	repo, err := e.InitWorkspaceRepo(ctx, ws)
	if err != nil {
		t.Fatalf("InitWorkspaceRepo: %v", err)
	}
	source := t.TempDir()
	gitFileTest(t, source, "init", "-q", "-b", "main")
	gitFileTest(t, source, "config", "user.name", "Evidence Test")
	gitFileTest(t, source, "config", "user.email", "evidence@example.test")
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFileTest(t, source, "add", "file.txt")
	gitFileTest(t, source, "commit", "-q", "-m", "initial")
	gitFileTest(t, source, "push", "-q", repo, "main")
}
func TestCaptureEvidenceRetainsUntrackedTreeAfterCheckoutRemoval(t *testing.T) {
	e := newUnitEngine(t)
	seedEvidenceWorkspace(t, e, "ws1")
	checkout, _, err := e.CreateRunCheckout(context.Background(), "ws1", "run-evidence", "main", "capture", "")
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(checkout, "new.txt"), []byte("retained\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	revision, err := e.CaptureEvidence(context.Background(), "run-evidence", "packet-a")
	if err != nil {
		t.Fatalf("CaptureEvidence: %v", err)
	}
	if revision.WorkspaceID != "ws1" || !validObjectID(revision.BaseCommit) ||
		!validObjectID(revision.Commit) || !validObjectID(revision.Tree) {
		t.Fatalf("revision identity = %+v", revision)
	}
	if len(revision.ChangedFiles) != 1 || revision.ChangedFiles[0].Path != "new.txt" ||
		revision.ChangedFiles[0].Additions != 1 || revision.ChangedFiles[0].Deletions != 0 {
		t.Fatalf("changed files = %+v", revision.ChangedFiles)
	}

	if removeErr := e.RemoveRunCheckout(context.Background(), "run-evidence"); removeErr != nil {
		t.Fatalf("RemoveRunCheckout: %v", removeErr)
	}
	patch, err := e.RenderEvidence(context.Background(), "ws1", revision.Commit, 0)
	if err != nil {
		t.Fatalf("RenderEvidence after cleanup: %v", err)
	}
	if patch.Base != revision.BaseCommit || !strings.Contains(patch.Text, "new.txt") ||
		!strings.Contains(patch.Text, "+retained") {
		t.Fatalf("retained patch = %+v", patch)
	}
}

func TestHostileCheckoutCannotExecuteDuringEvidenceAndDiff(t *testing.T) {
	e := newUnitEngine(t)
	seedEvidenceWorkspace(t, e, "ws-hostile")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws-hostile", "run-hostile-evidence", "main", "hostile evidence", "")
	if err != nil {
		t.Fatal(err)
	}

	markerDir := t.TempDir()
	markers := map[string]string{
		"fsmonitor": filepath.Join(markerDir, "fsmonitor-ran"),
		"filter":    filepath.Join(markerDir, "filter-ran"),
		"hook":      filepath.Join(markerDir, "hook-ran"),
		"external":  filepath.Join(markerDir, "external-ran"),
		"textconv":  filepath.Join(markerDir, "textconv-ran"),
	}
	writeMarkerCommand := func(name, suffix string) string {
		t.Helper()
		path := filepath.Join(markerDir, name)
		body := "#!/bin/sh\nprintf " + suffix + " > " + markers[name] + "\n"
		if name == "filter" || name == "textconv" {
			body += "cat\n"
		}
		body += "exit 1\n"
		if writeErr := os.WriteFile(path, []byte(body), 0o755); writeErr != nil {
			t.Fatal(writeErr)
		}
		return path
	}
	fsmonitor := writeMarkerCommand("fsmonitor", "fsmonitor")
	filter := writeMarkerCommand("filter", "filter")
	hook := writeMarkerCommand("hook", "hook")
	external := writeMarkerCommand("external", "external")
	textconv := writeMarkerCommand("textconv", "textconv")
	hooks := filepath.Join(markerDir, "hooks")
	if mkdirErr := os.Mkdir(hooks, 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if renameErr := os.Rename(hook, filepath.Join(hooks, "pre-commit")); renameErr != nil {
		t.Fatal(renameErr)
	}

	for _, config := range [][2]string{
		{"core.fsmonitor", fsmonitor},
		{"core.hooksPath", hooks},
		{"filter.hostile.clean", filter},
		{"filter.hostile.required", "true"},
		{"diff.external", external},
		{"diff.hostile.textconv", textconv},
	} {
		gitFileTest(t, checkout, "config", config[0], config[1])
	}
	if writeErr := os.WriteFile(filepath.Join(checkout, ".gitattributes"), []byte("payload filter=hostile diff=hostile\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if mkdirErr := os.MkdirAll(filepath.Join(checkout, ".git", "info"), 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := os.WriteFile(filepath.Join(checkout, ".git", "info", "attributes"), []byte("payload filter=hostile diff=hostile\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if writeErr := os.WriteFile(filepath.Join(checkout, "payload"), []byte("payload body\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	revision, err := e.CaptureEvidence(ctx, "run-hostile-evidence", "packet-hostile")
	if err != nil {
		t.Fatalf("CaptureEvidence: %v", err)
	}
	evidencePatch, err := e.RenderEvidence(ctx, "ws-hostile", revision.Commit, 0)
	if err != nil {
		t.Fatalf("RenderEvidence: %v", err)
	}
	if !strings.Contains(evidencePatch.Text, "+payload body") {
		t.Fatalf("evidence patch lost raw payload content:\n%s", evidencePatch.Text)
	}

	first, err := e.writeSnapshotTree(ctx, "run-hostile-evidence", checkout)
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(checkout, "payload"), []byte("payload changed\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	second, err := e.writeSnapshotTree(ctx, "run-hostile-evidence", checkout)
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	interval, err := e.RunPatch(ctx, "run-hostile-evidence", PatchRequest{From: first, To: second})
	if err != nil {
		t.Fatalf("snapshot interval patch: %v", err)
	}
	if !strings.Contains(interval.Text, "+payload changed") {
		t.Fatalf("snapshot patch lost raw payload content:\n%s", interval.Text)
	}
	cumulative, err := e.RunPatch(ctx, "run-hostile-evidence", PatchRequest{})
	if err != nil {
		t.Fatalf("cumulative patch: %v", err)
	}
	if !strings.Contains(cumulative.Text, "+payload changed") {
		t.Fatalf("cumulative patch lost raw payload content:\n%s", cumulative.Text)
	}
	for name, marker := range markers {
		if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
			t.Errorf("planted %s command ran, marker stat = %v", name, statErr)
		}
	}
}

func TestRemoveEvidenceDeletesOnlyExactPacketRef(t *testing.T) {
	e := newUnitEngine(t)
	seedEvidenceWorkspace(t, e, "ws1")
	checkout, _, err := e.CreateRunCheckout(context.Background(), "ws1", "run-evidence-gc", "main", "capture", "")
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(checkout, "new.txt"), []byte("retained\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	a, err := e.CaptureEvidence(context.Background(), "run-evidence-gc", "packet-a")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := e.existingRepoPath("ws1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(context.Background(), repo, "update-ref", evidenceRef("packet-b"), a.Commit); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveEvidence(context.Background(), "ws1", "packet-a"); err != nil {
		t.Fatalf("RemoveEvidence: %v", err)
	}
	if _, found, err := e.evidenceRefCommit(context.Background(), repo, evidenceRef("packet-a")); err != nil || found {
		t.Fatalf("packet-a ref after removal = found %v, err %v", found, err)
	}
	if _, found, err := e.evidenceRefCommit(context.Background(), repo, evidenceRef("packet-b")); err != nil || !found {
		t.Fatalf("packet-b ref after removal = found %v, err %v", found, err)
	}
}

func TestEvidenceRejectsMaliciousIdentifiers(t *testing.T) {
	e := newUnitEngine(t)
	ctx := context.Background()
	for _, run := range []domain.RunID{"", "../outside", "a/b", "a\\b", ".."} {
		if _, err := e.CaptureEvidence(ctx, run, "packet"); err == nil {
			t.Errorf("CaptureEvidence(%q) accepted malicious run id", run)
		}
	}
	for _, packet := range []string{"", "../outside", "a/b", "a\\b", "..", "a..b"} {
		if _, err := e.CaptureEvidence(ctx, "run", packet); !errors.Is(err, ErrInvalidEvidenceID) {
			t.Errorf("CaptureEvidence packet %q = %v, want ErrInvalidEvidenceID", packet, err)
		}
		if err := e.RemoveEvidence(ctx, "ws1", packet); !errors.Is(err, ErrInvalidEvidenceID) {
			t.Errorf("RemoveEvidence packet %q = %v, want ErrInvalidEvidenceID", packet, err)
		}
	}
	for _, commit := range []string{"HEAD", "../../etc", "0123456789", strings.Repeat("A", 40)} {
		if _, err := e.RenderEvidence(ctx, "ws1", commit, 100); !errors.Is(err, ErrInvalidObjectID) {
			t.Errorf("RenderEvidence commit %q = %v, want ErrInvalidObjectID", commit, err)
		}
	}
}
func TestParseDiffCountRejectsMalformedAndOverflow(t *testing.T) {
	for _, value := range []string{"", "not-a-count", "999999999999999999999999999999999999999999"} {
		if _, err := parseDiffCount(value); err == nil {
			t.Errorf("parseDiffCount(%q) = nil error, want rejection", value)
		}
	}
	if got, err := parseDiffCount("-"); err != nil || got != 0 {
		t.Fatalf("parseDiffCount(-) = %d, %v, want zero", got, err)
	}
}
func TestParseEvidenceDiffStatsBoundsOutputAndFacts(t *testing.T) {
	path := strings.Repeat("p", evidenceDiffPathBytesLimit)
	record := "1\t0\t" + path + "\x00"
	raw := strings.Repeat(record, evidenceDiffItemsLimit+1)
	if len(raw) <= evidenceDiffBytesLimit {
		t.Fatalf("test numstat output = %d bytes, want over %d", len(raw), evidenceDiffBytesLimit)
	}
	bounded := raw[:evidenceDiffBytesLimit]
	files, truncated, err := parseEvidenceDiffStats(bounded, true)
	if err != nil {
		t.Fatalf("parse bounded numstat: %v", err)
	}
	if !truncated {
		t.Fatal("bounded numstat did not report truncation")
	}
	if len(files) > evidenceDiffItemsLimit {
		t.Fatalf("parsed %d changed files, want at most %d", len(files), evidenceDiffItemsLimit)
	}
	for _, file := range files {
		if len(file.Path) > evidenceDiffPathBytesLimit {
			t.Fatalf("changed path length = %d, want at most %d", len(file.Path), evidenceDiffPathBytesLimit)
		}
	}

	items := strings.Repeat("1\t0\tfile\x00", evidenceDiffItemsLimit+1)
	files, truncated, err = parseEvidenceDiffStats(items, false)
	if err != nil {
		t.Fatalf("parse item-limited numstat: %v", err)
	}
	if !truncated || len(files) != evidenceDiffItemsLimit {
		t.Fatalf("item-limited numstat = %d files, truncated=%v", len(files), truncated)
	}
}

type cancellationGate struct {
	context.Context
	first   chan struct{}
	release chan struct{}
	calls   int
}

func (c *cancellationGate) Err() error {
	c.calls++
	if c.calls == 1 {
		close(c.first)
		<-c.release
		return nil
	}
	return context.Canceled
}

func TestEvidenceCaptureHonorsCancellationWhileWaitingForWriter(t *testing.T) {
	e := newUnitEngine(t)
	e.fileWriteMu.Lock()
	ctx := &cancellationGate{
		Context: context.Background(),
		first:   make(chan struct{}),
		release: make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.CaptureEvidence(ctx, "run", "packet")
		done <- err
	}()
	<-ctx.first
	close(ctx.release)
	e.fileWriteMu.Unlock()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("CaptureEvidence canceled after writer wait = %v, want context.Canceled", err)
	}
}
func TestCaptureEvidenceRejectsOversizedCheckoutBeforeStaging(t *testing.T) {
	e := newUnitEngine(t)
	seedEvidenceWorkspace(t, e, "ws-large")
	checkout, _, err := e.CreateRunCheckout(context.Background(), "ws-large", "run-large", "main", "capture", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(checkout, "oversized.bin")
	if writeErr := os.WriteFile(path, nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if truncateErr := os.Truncate(path, MaxEvidenceInputBytes+1); truncateErr != nil {
		t.Fatal(truncateErr)
	}
	_, err = e.CaptureEvidence(context.Background(), "run-large", "packet-large")
	if !errors.Is(err, ErrEvidenceStorageLimit) {
		t.Fatalf("oversized capture error = %v, want ErrEvidenceStorageLimit", err)
	}
}

func TestEvidenceSnapshotBoundsRejectsOversizedTree(t *testing.T) {
	e := newUnitEngine(t)
	seedEvidenceWorkspace(t, e, "ws-snapshot-large")
	checkout, _, err := e.CreateRunCheckout(t.Context(), "ws-snapshot-large", "run-snapshot-large", "main", "capture", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(checkout, "oversized.bin")
	if writeErr := os.WriteFile(path, nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if truncateErr := os.Truncate(path, MaxEvidenceInputBytes+1); truncateErr != nil {
		t.Fatal(truncateErr)
	}
	tree, err := e.writeSnapshotTree(t.Context(), "run-snapshot-large", checkout)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.checkEvidenceSnapshotBounds(t.Context(), "run-snapshot-large", checkout, tree); !errors.Is(err, ErrEvidenceStorageLimit) {
		t.Fatalf("snapshot bounds error = %v, want ErrEvidenceStorageLimit", err)
	}
}

func TestCaptureEvidenceCountsLeadingSpacePath(t *testing.T) {
	e := newUnitEngine(t)
	seedEvidenceWorkspace(t, e, "ws-leading-space")
	checkout, _, err := e.CreateRunCheckout(context.Background(), "ws-leading-space", "run-leading-space", "main", "capture", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(checkout, " oversized.bin")
	if writeErr := os.WriteFile(path, nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if truncateErr := os.Truncate(path, MaxEvidenceInputBytes+1); truncateErr != nil {
		t.Fatal(truncateErr)
	}
	_, err = e.CaptureEvidence(context.Background(), "run-leading-space", "packet-leading-space")
	if !errors.Is(err, ErrEvidenceStorageLimit) {
		t.Fatalf("leading-space oversized capture error = %v, want ErrEvidenceStorageLimit", err)
	}
}

func TestCaptureEvidenceIgnoresLargeIgnoredFile(t *testing.T) {
	e := newUnitEngine(t)
	seedEvidenceWorkspace(t, e, "ws-ignored-large")
	checkout, _, err := e.CreateRunCheckout(context.Background(), "ws-ignored-large", "run-ignored-large", "main", "capture", "")
	if err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(checkout, ".gitignore")
	if err := os.WriteFile(ignore, []byte("ignored.bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFileTest(t, checkout, "add", ".gitignore")
	ignored := filepath.Join(checkout, "ignored.bin")
	if err := os.WriteFile(ignored, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(ignored, MaxEvidenceInputBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CaptureEvidence(context.Background(), "run-ignored-large", "packet-ignored-large"); err != nil {
		t.Fatalf("capture with ignored oversized file: %v", err)
	}
}

func TestPublishRunBranchUsesLiveCheckoutHead(t *testing.T) {
	e := newUnitEngine(t)
	seedEvidenceWorkspace(t, e, "ws-publish-head")
	ctx := t.Context()
	checkout, branch, err := e.CreateRunCheckout(ctx, "ws-publish-head", "run-publish-head", "main", "publish", "")
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(checkout, "published.txt"), []byte("published\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	gitFileTest(t, checkout, "config", "user.name", "Publish Test")
	gitFileTest(t, checkout, "config", "user.email", "publish@example.test")
	gitFileTest(t, checkout, "add", "-A")
	gitFileTest(t, checkout, "commit", "-q", "-m", "publish")
	want, err := e.checkoutHead(ctx, "run-publish-head", checkout)
	if err != nil {
		t.Fatalf("resolve checkout HEAD: %v", err)
	}
	var published string
	e.cfg.OnBranchPublished = func(_ domain.RunID, commit string, _ time.Time) { published = commit }
	got, err := e.PublishRunBranch(ctx, "run-publish-head")
	if err != nil {
		t.Fatalf("PublishRunBranch: %v", err)
	}
	if got != want || published != want {
		t.Fatalf("published tip = %q/callback %q, want live HEAD %q", got, published, want)
	}
	repo, err := e.existingRepoPath("ws-publish-head")
	if err != nil {
		t.Fatal(err)
	}
	if tip, err := e.git(ctx, repo, "rev-parse", "--verify", "refs/heads/"+branch); err != nil || tip != want {
		t.Fatalf("workspace run branch = %q (%v), want %q", tip, err, want)
	}
}
