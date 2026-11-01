//go:build integration

package gitengine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// upstreamRun is a run on a workspace whose upstream main has two commits.
// Its timers never fire, so the only snapshot is the one StopDiffWatch takes.
type upstreamRun struct {
	e        *Engine
	diffs    events.Subscription
	src      string
	url      string
	checkout string
	run      domain.RunID
	fork     string
}

func newUpstreamRun(t *testing.T) *upstreamRun {
	t.Helper()
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	dir := t.TempDir()
	e, err := New(Config{
		ReposDir:     filepath.Join(dir, "repos"),
		CheckoutsDir: filepath.Join(dir, "checkouts"),
		Bus:          bus,
		QuietPeriod:  time.Hour,
		MinInterval:  time.Hour,
		MaxInterval:  2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	url := serveTransport(t, e)
	if _, err = e.InitWorkspaceRepo(t.Context(), "ws1"); err != nil {
		t.Fatal(err)
	}
	r := &upstreamRun{e: e, src: t.TempDir(), run: "run-upstream"}
	gitc(t, r.src, "init")
	r.url = url("ws1")
	r.upstreamCommit(t, "seed1.txt")
	r.upstreamCommit(t, "seed2.txt")
	r.checkout, _, err = e.CreateRunCheckout(t.Context(), "ws1", r.run, "main", "upstream", "")
	if err != nil {
		t.Fatal(err)
	}
	r.fork = bareRevParse(t, e, "ws1", "refs/heads/main")
	r.diffs = subscribeTypes(t, bus, events.TypeRunDiff)
	if err = e.StartDiffWatch(t.Context(), "ws1", r.run); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *upstreamRun) upstreamCommit(t *testing.T, name string) string {
	t.Helper()
	writeFile(t, filepath.Join(r.src, name), "upstream\n")
	gitc(t, r.src, "add", "-A")
	gitc(t, r.src, "commit", "-m", "upstream "+name)
	gitc(t, r.src, "push", r.url, "main")
	return gitc(t, r.src, "rev-parse", "HEAD")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *upstreamRun) commitOwn(t *testing.T, name string) {
	t.Helper()
	writeFile(t, filepath.Join(r.checkout, name), "own\n")
	gitc(t, r.checkout, "add", "-A")
	gitc(t, r.checkout, "commit", "-m", "own "+name)
}

// finalDiff stops the watch and returns the run.diff its final snapshot
// published.
func (r *upstreamRun) finalDiff(t *testing.T) events.RunDiffPayload {
	t.Helper()
	r.e.StopDiffWatch(r.run)
	ev, ok := nextEvent(t, r.diffs, time.Second)
	if !ok {
		t.Fatal("stopping the watch published no run.diff")
	}
	return ev.Payload.(events.RunDiffPayload)
}

func paths(files []events.FileDiffStat) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func TestDiffExcludesMergedUpstream(t *testing.T) {
	for _, tc := range []struct {
		name string
		sync func(t *testing.T, r *upstreamRun)
	}{
		{"merge", func(t *testing.T, r *upstreamRun) { gitc(t, r.checkout, "merge", "-m", "sync", "origin/main") }},
		{"rebase", func(t *testing.T, r *upstreamRun) { gitc(t, r.checkout, "rebase", "origin/main") }},
		{"merge with packed refs", func(t *testing.T, r *upstreamRun) {
			gitc(t, r.checkout, "merge", "-m", "sync", "origin/main")
			gitc(t, r.checkout, "pack-refs", "--all")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newUpstreamRun(t)
			r.commitOwn(t, "own.txt")
			r.upstreamCommit(t, "up1.txt")
			tip := r.upstreamCommit(t, "up2.txt")
			gitc(t, r.checkout, "fetch", "origin")
			tc.sync(t, r)
			writeFile(t, filepath.Join(r.checkout, "wip.txt"), "wip\n")

			patch, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if patch.Base != tip {
				t.Errorf("patch base = %s, want upstream tip %s", patch.Base, tip)
			}
			if !strings.Contains(patch.Text, "own.txt") || !strings.Contains(patch.Text, "wip.txt") || strings.Contains(patch.Text, "up1.txt") {
				t.Errorf("live patch should list only the session's own files:\n%s", patch.Text)
			}

			final := r.finalDiff(t)
			if final.Base != tip {
				t.Errorf("run.diff base = %s, want %s", final.Base, tip)
			}
			if got := paths(final.Files); !slices.Equal(got, []string{"own.txt", "wip.txt"}) {
				t.Errorf("run.diff files = %v, want own.txt and wip.txt", got)
			}

			if err = r.e.RemoveRunCheckout(t.Context(), r.run); err != nil {
				t.Fatal(err)
			}
			recorded, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if !recorded.Recorded || !strings.Contains(recorded.Text, "own.txt") ||
				!strings.Contains(recorded.Text, "wip.txt") || strings.Contains(recorded.Text, "up1.txt") {
				t.Errorf("recorded patch should list only the session's own files:\n%s", recorded.Text)
			}
		})
	}
}

func TestDiffFallsBackToForkPoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(t *testing.T, r *upstreamRun)
	}{
		{"unrelated history", func(t *testing.T, r *upstreamRun) {
			tree := gitc(t, r.checkout, "hash-object", "-t", "tree", os.DevNull)
			orphan := gitc(t, r.checkout, "commit-tree", tree, "-m", "orphan")
			gitc(t, r.checkout, "update-ref", "refs/remotes/origin/main", orphan)
		}},
		{"older than the fork point", func(t *testing.T, r *upstreamRun) {
			gitc(t, r.checkout, "update-ref", "refs/remotes/origin/main", r.fork+"^")
		}},
		{"garbage", func(t *testing.T, r *upstreamRun) {
			writeFile(t, filepath.Join(r.checkout, ".git", "refs", "remotes", "origin", "main"), "not an object id\n")
		}},
		{"missing", func(t *testing.T, r *upstreamRun) {
			gitc(t, r.checkout, "update-ref", "-d", "refs/remotes/origin/main")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newUpstreamRun(t)
			r.commitOwn(t, "own.txt")
			tc.set(t, r)
			patch, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if patch.Base != r.fork {
				t.Errorf("patch base = %s, want fork point %s", patch.Base, r.fork)
			}
			if final := r.finalDiff(t); final.Base != r.fork {
				t.Errorf("run.diff base = %s, want fork point %s", final.Base, r.fork)
			}
		})
	}
}

func TestStopDiffWatchSnapshotsPendingChanges(t *testing.T) {
	r := newUpstreamRun(t)
	writeFile(t, filepath.Join(r.checkout, "last-second.txt"), "late\n")

	final := r.finalDiff(t)
	if got := paths(final.Files); !slices.Equal(got, []string{"last-second.txt"}) {
		t.Fatalf("final run.diff files = %v, want last-second.txt", got)
	}
	if err := r.e.RemoveRunCheckout(t.Context(), r.run); err != nil {
		t.Fatal(err)
	}
	recorded, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorded.Text, "last-second.txt") {
		t.Errorf("recorded patch lost the last file:\n%s", recorded.Text)
	}
}

func TestStopDiffWatchWithoutChangesPublishesNothing(t *testing.T) {
	r := newUpstreamRun(t)
	r.e.StopDiffWatch(r.run)
	if ev, ok := nextEvent(t, r.diffs, 300*time.Millisecond); ok {
		t.Fatalf("unchanged run published %+v", ev.Payload)
	}
}

func TestUpstreamAcceptingTheRunClearsItsStats(t *testing.T) {
	r := newUpstreamRun(t)
	r.commitOwn(t, "own.txt")
	if got := paths(r.finalDiff(t).Files); !slices.Equal(got, []string{"own.txt"}) {
		t.Fatalf("run.diff files = %v, want own.txt", got)
	}
	if err := r.e.StartDiffWatch(t.Context(), "ws1", r.run); err != nil {
		t.Fatal(err)
	}
	head := gitc(t, r.checkout, "rev-parse", "HEAD")
	gitc(t, r.checkout, "push", r.url, "HEAD:main")
	gitc(t, r.checkout, "fetch", "origin")

	final := r.finalDiff(t)
	if final.Base != head || len(final.Files) != 0 {
		t.Fatalf("run.diff after upstream took the session = base %s files %v, want base %s and no files", final.Base, paths(final.Files), head)
	}
	if err := r.e.RemoveRunCheckout(t.Context(), r.run); err != nil {
		t.Fatal(err)
	}
	recorded, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Text != "" {
		t.Errorf("recorded patch after upstream took the session should be empty:\n%s", recorded.Text)
	}
}

func TestRestartedWatchRepublishesStatsForAMovedBase(t *testing.T) {
	r := newUpstreamRun(t)
	r.commitOwn(t, "own.txt")
	r.finalDiff(t)
	head := gitc(t, r.checkout, "rev-parse", "HEAD")
	gitc(t, r.checkout, "push", r.url, "HEAD:main")
	gitc(t, r.checkout, "fetch", "origin")
	if err := r.e.StartDiffWatch(t.Context(), "ws1", r.run); err != nil {
		t.Fatal(err)
	}

	final := r.finalDiff(t)
	if final.Base != head || len(final.Files) != 0 {
		t.Fatalf("run.diff after restart = base %s files %v, want base %s and no files", final.Base, paths(final.Files), head)
	}
}

func TestRecordedPatchKeepsTheBaseOfItsSnapshot(t *testing.T) {
	r := newUpstreamRun(t)
	r.commitOwn(t, "own.txt")
	r.finalDiff(t)
	if err := r.e.StartDiffWatch(t.Context(), "ws1", r.run); err != nil {
		t.Fatal(err)
	}
	r.upstreamCommit(t, "up1.txt")
	gitc(t, r.checkout, "fetch", "origin")
	gitc(t, r.checkout, "merge", "-m", "sync", "origin/main")
	// The final capture fails, so the last published snapshot stays the one
	// measured against the fork point.
	large, err := os.Create(filepath.Join(r.checkout, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err = large.Truncate(MaxSnapshotInputBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err = large.Close(); err != nil {
		t.Fatal(err)
	}
	r.e.StopDiffWatch(r.run)

	if err = r.e.RemoveRunCheckout(t.Context(), r.run); err != nil {
		t.Fatal(err)
	}
	recorded, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorded.Text, "own.txt") || strings.Contains(recorded.Text, "up1.txt") {
		t.Errorf("recorded patch should be the last snapshot against its own base:\n%s", recorded.Text)
	}
}
