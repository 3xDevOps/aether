package coord

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// The agent is told to read the co-author list before every commit, and
// the list is rewritten under it while the run is live. A reader that
// catches the rewrite has to see one whole list or the other, never a
// missing path: an ENOENT there would silently cost the branch its
// trailers.
func TestWriteCoAuthorsIsAtomic(t *testing.T) {
	h := newHarness(t, 1)
	run := h.runs[0].ID
	if _, err := h.svc.Provision(context.Background(), run, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	path := filepath.Join(h.dir, "coord", string(run), CoAuthorsName)
	if err := h.svc.WriteCoAuthors(run, []string{"Co-authored-by: Ada <ada@example.com>"}); err != nil {
		t.Fatalf("WriteCoAuthors: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	bad := make(chan string, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			body, err := os.ReadFile(path)
			if err != nil {
				select {
				case bad <- "read: " + err.Error():
				default:
				}
				return
			}
			// Every line is a whole trailer, so a torn write shows up as a
			// line that is not one.
			for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
				if line != "" && !strings.HasPrefix(line, "Co-authored-by: ") {
					select {
					case bad <- "torn line: " + line:
					default:
					}
					return
				}
			}
		}
	}()

	for i := range 200 {
		trailers := []string{"Co-authored-by: Ada <ada@example.com>"}
		if i%2 == 0 {
			trailers = append(trailers, "Co-authored-by: Bob <bob@example.com>")
		}
		if err := h.svc.WriteCoAuthors(run, trailers); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("WriteCoAuthors: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case why := <-bad:
		t.Fatalf("a reader caught the rewrite: %s", why)
	default:
	}

	if got := mode(t, path); got != configMode {
		t.Errorf("co-author list mode = %04o after rewrites, want %04o", got, configMode)
	}
	// The temp files a rename leaves behind would be mounted into the
	// container beside the list.
	entries, err := os.ReadDir(filepath.Join(h.dir, "coord", string(run)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "."+CoAuthorsName) {
			t.Errorf("rewrite left %s behind", entry.Name())
		}
	}
}

// An empty list is a file with nothing in it, not a missing one: the agent
// reads the path unconditionally.
func TestWriteCoAuthorsEmptyList(t *testing.T) {
	h := newHarness(t, 1)
	run := h.runs[0].ID
	if _, err := h.svc.Provision(context.Background(), run, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := h.svc.WriteCoAuthors(run, nil); err != nil {
		t.Fatalf("WriteCoAuthors: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(h.dir, "coord", string(run), CoAuthorsName))
	if err != nil || len(body) != 0 {
		t.Fatalf("empty co-author list = (%q, %v), want an empty file", body, err)
	}
}

// Writing for a run that was never provisioned has to fail: the caller
// would otherwise believe the agent was told.
func TestWriteCoAuthorsUnprovisionedRun(t *testing.T) {
	h := newHarness(t, 1)
	if err := h.svc.WriteCoAuthors(domain.RunID("nosuchrun"), nil); err == nil {
		t.Fatal("WriteCoAuthors for an unprovisioned run returned nil")
	}
}
