package helper

import (
	"bytes"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/guest/guestpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reconnectingGit refuses the server's first watch, which leaves the server
// waiting out its retry backoff, and holds the second until a Flush waits,
// so the reconnect cannot beat the last snapshot.
type reconnectingGit struct {
	*gitService
	tries   atomic.Int32
	refused chan struct{}
}

func (g *reconnectingGit) Watch(stream grpc.BidiStreamingServer[guestpb.WatchAck, guestpb.WatchEvent]) error {
	switch g.tries.Add(1) {
	case 1:
		close(g.refused)
		return status.Error(codes.Unavailable, "the git service is not ready")
	case 2:
		for {
			g.mu.Lock()
			_, waiting := g.watched[string(testRun)]
			g.mu.Unlock()
			if waiting {
				break
			}
			select {
			case <-stream.Context().Done():
				return stream.Context().Err()
			case <-time.After(time.Millisecond):
			}
		}
	}
	return g.gitService.Watch(stream)
}

func TestAStopWhileTheWatchReconnectsRecordsTheLastSnapshot(t *testing.T) {
	git := &reconnectingGit{refused: make(chan struct{})}
	p := newGitPair(t, func(real *gitService) guestpb.GitServer { git.gitService = real; return git })
	ctx := testContext(t)
	if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
		t.Fatal(err)
	}
	select {
	case <-git.refused:
	case <-ctx.Done():
		t.Fatal("the server never opened its watch")
	}
	if err := os.WriteFile(filepath.Join(p.checkout, "last.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.host.StopDiffWatch(testRun)
	store := filepath.Join(filepath.Dir(p.repo), "..", "checkouts", string(testRun)+".diffsnap")
	if got := runGitQuiet(store, "ls-tree", "--name-only", "refs/aether/published", "last.txt"); got != "last.txt" {
		t.Fatalf("the last recorded snapshot lacks the file written before a stop during the watch's reconnect: %q", got)
	}
}

func TestAStopDoesNotWaitForAWatchThatCannotStart(t *testing.T) {
	p := newGitPair(t, nil)
	ctx := testContext(t)
	// An identity record that puts the checkout in a VM fails each watch of
	// it in WatchCheckout, as this service runs no VMs.
	record := filepath.Join(p.guestDir, string(testRun)+".json")
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(record, bytes.Replace(data, []byte("{"), []byte(`{"vm":true,`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p.checkout, "last.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	p.host.StopDiffWatch(testRun)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("a stop whose watch cannot start took %s", took)
	}
	if _, err = os.Stat(filepath.Join(p.guestDir, string(testRun)+".diffsnap", "history-gap")); err != nil {
		t.Fatalf("the last snapshot is no history gap: %v", err)
	}
}
