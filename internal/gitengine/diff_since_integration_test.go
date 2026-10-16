//go:build integration

package gitengine

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// checkpoint snapshots the run and resumes its watch, so the next one starts
// from the snapshot it recorded.
func (r *upstreamRun) checkpoint(t *testing.T) (tree, head, subject string) {
	t.Helper()
	d := r.finalDiff(t)
	if err := r.e.StartDiffWatch(t.Context(), "ws1", r.run); err != nil {
		t.Fatal(err)
	}
	return d.Tree, d.Head, d.HeadSubject
}

func TestRunPatchSinceCheckpoint(t *testing.T) {
	r := newUpstreamRun(t)
	r.commitOwn(t, "before.txt")
	tree, head, subject := r.checkpoint(t)
	if want := gitc(t, r.checkout, "rev-parse", "HEAD"); head != want {
		t.Errorf("run.diff head = %q, want %q", head, want)
	}
	if subject != "own before.txt" {
		t.Errorf("run.diff head_subject = %q, want %q", subject, "own before.txt")
	}

	writeFile(t, filepath.Join(r.checkout, "seed1.txt"), "edited\n")
	writeFile(t, filepath.Join(r.checkout, "untracked.txt"), "new\n")
	live, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{From: tree})
	if err != nil {
		t.Fatal(err)
	}
	if live.Recorded || live.Base != tree || live.ForkTree != "" {
		t.Errorf("live from-only patch = recorded %v base %s fork_tree %q", live.Recorded, live.Base, live.ForkTree)
	}
	if !strings.Contains(live.Text, "seed1.txt") || !strings.Contains(live.Text, "untracked.txt") || strings.Contains(live.Text, "before.txt") {
		t.Errorf("live patch should list only changes since the checkpoint:\n%s", live.Text)
	}

	cumulative, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if want := gitc(t, r.checkout, "rev-parse", r.fork+"^{tree}"); cumulative.ForkTree != want {
		t.Errorf("fork_tree = %q, want %q", cumulative.ForkTree, want)
	}
	r.checkpoint(t)
	sinceStart, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{From: cumulative.ForkTree})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sinceStart.Text, "before.txt") || !strings.Contains(sinceStart.Text, "untracked.txt") {
		t.Errorf("since the run started should list every change:\n%s", sinceStart.Text)
	}

	r.e.StopDiffWatch(r.run)
	if err = r.e.RemoveRunCheckout(t.Context(), r.run); err != nil {
		t.Fatal(err)
	}
	recorded, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{From: tree})
	if err != nil {
		t.Fatal(err)
	}
	if !recorded.Recorded || recorded.Base != tree || recorded.ForkTree != "" ||
		!strings.Contains(recorded.Text, "seed1.txt") || !strings.Contains(recorded.Text, "untracked.txt") || strings.Contains(recorded.Text, "before.txt") {
		t.Errorf("recorded from-only patch = %+v", recorded)
	}
	recordedStart, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{From: cumulative.ForkTree})
	if err != nil {
		t.Fatal(err)
	}
	if !recordedStart.Recorded || !strings.Contains(recordedStart.Text, "before.txt") {
		t.Errorf("recorded since-start patch = %+v", recordedStart)
	}
	whole, err := r.e.RunPatch(t.Context(), r.run, PatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if whole.ForkTree != cumulative.ForkTree {
		t.Errorf("recorded fork_tree = %q, want %q", whole.ForkTree, cumulative.ForkTree)
	}
}

func TestRunPatchRejectsUnrecordedFrom(t *testing.T) {
	r := newUpstreamRun(t)
	r.commitOwn(t, "own.txt")
	r.checkpoint(t)
	unrecorded := gitc(t, r.checkout, "rev-parse", r.fork+"^^{tree}")
	for _, tc := range []struct {
		name string
		req  PatchRequest
		want error
	}{
		{"commit", PatchRequest{From: r.fork}, ErrInvalidObjectID},
		{"garbage", PatchRequest{From: "not-an-object"}, ErrInvalidObjectID},
		{"to alone", PatchRequest{To: unrecorded}, ErrInvalidObjectID},
		{"unrecorded tree", PatchRequest{From: unrecorded}, ErrSnapshotTreeMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.e.RunPatch(t.Context(), r.run, tc.req); !errors.Is(err, tc.want) {
				t.Errorf("RunPatch(%+v) error = %v, want %v", tc.req, err, tc.want)
			}
		})
	}
}

func TestRunDiffHeadSubjectIsBounded(t *testing.T) {
	r := newUpstreamRun(t)
	writeFile(t, filepath.Join(r.checkout, "long.txt"), "x\n")
	gitc(t, r.checkout, "add", "-A")
	gitc(t, r.checkout, "commit", "-m", strings.Repeat("€", 70))
	if _, _, subject := r.checkpoint(t); subject != strings.Repeat("€", 66) {
		t.Errorf("head_subject = %q, want 66 euro signs", subject)
	}
}
