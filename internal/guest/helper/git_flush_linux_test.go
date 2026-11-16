package helper

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// refusingGit refuses the server's first watch, which leaves the server
// waiting out its retry backoff.
type refusingGit struct {
	*gitService
	once    sync.Once
	refused chan struct{}
}

func (g *refusingGit) Watch(stream grpc.BidiStreamingServer[guestpb.WatchAck, guestpb.WatchEvent]) error {
	refuse := false
	g.once.Do(func() { refuse = true })
	if refuse {
		close(g.refused)
		return status.Error(codes.Unavailable, "the git service is not ready")
	}
	return g.gitService.Watch(stream)
}

func TestAStopWhileTheWatchReconnectsRecordsTheLastSnapshot(t *testing.T) {
	git := &refusingGit{refused: make(chan struct{})}
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
