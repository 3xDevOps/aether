package gitengine

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

// errCountBudget refuses an untracked file the stat set has no budget left
// to read.
var errCountBudget = errors.New("gitengine: untracked content exceeds the stat set's read budget")

var rawStatus = map[byte]string{'A': "added", 'D': "deleted"}

// diffStats builds the snapshot stat set: raw status and numstat against
// base for tracked work (committed and uncommitted) plus untracked files,
// added at their line counts. Both listings use -z (NUL-separated, unquoted) so non-ASCII
// paths survive verbatim, and --no-renames so a rename reports its real
// old and new paths rather than a munged "old => new". Never mutates the
// index. Sorted by path.
//
// Every changed path is listed, because the overlap index reads them. What
// is bounded is the untracked content read to count lines: at most
// MaxSnapshotInputBytes, the most a snapshot would stage. truncated reports
// a file left unread at zero, which makes the totals a floor.
func (e *Engine) diffStats(ctx context.Context, run domain.RunID, checkout, base string) (files []events.FileDiffStat, truncated bool, err error) {
	out, err := e.gitCheckout(ctx, run, checkout, "diff", "--raw", "--numstat", "--no-renames", "-z", base)
	if err != nil {
		return nil, false, err
	}
	files = make([]events.FileDiffStat, 0, 8)
	status := map[string]string{}
	tokens := strings.Split(out, "\x00")
	for i := 0; i < len(tokens); i++ {
		if strings.HasPrefix(tokens[i], ":") && i+1 < len(tokens) {
			status[tokens[i+1]] = cmp.Or(rawStatus[tokens[i][len(tokens[i])-1]], "modified")
			i++
			continue
		}
		parts := strings.SplitN(tokens[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		add, _ := strconv.Atoi(parts[0]) // "-" (binary) parses as 0
		del, _ := strconv.Atoi(parts[1])
		files = append(files, events.FileDiffStat{Path: parts[2], Additions: add, Deletions: del, Status: status[parts[2]]})
	}
	untracked, _, err := e.gitCheckoutBounded(ctx, run, checkout, -1, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, false, err
	}
	checkoutRoot, err := os.OpenRoot(checkout)
	if err != nil {
		return nil, false, fmt.Errorf("gitengine: open checkout for diff stats: %w", err)
	}
	defer func() { _ = checkoutRoot.Close() }()
	budget := int64(MaxSnapshotInputBytes)
	for path := range strings.SplitSeq(untracked, "\x00") {
		if path == "" {
			continue
		}
		lines, read, err := countLines(ctx, checkoutRoot, path, budget)
		if errors.Is(err, errCountBudget) {
			truncated = true
		} else if err != nil {
			continue
		}
		budget -= read
		files = append(files, events.FileDiffStat{Path: path, Additions: lines, Status: "added"})
	}
	slices.SortFunc(files, func(a, b events.FileDiffStat) int {
		return strings.Compare(a.Path, b.Path)
	})
	return files, truncated, nil
}

// countBytesCap bounds how much of a single untracked file a snapshot will
// read; beyond it the line count is truncated. Keeps multi-GB build
// artifacts from stalling the watch loop.
const countBytesCap = 8 << 20

// countLines counts the lines in an untracked path (a trailing partial line
// counts) and reports how many bytes it read to do so. A symlink counts as
// one line (git's view of its content) and is never followed - the target
// may be a FIFO or device that would block the watch loop forever. Only
// regular files are read, capped at countBytesCap, and one whose read would
// not fit in budget is refused unread with errCountBudget.
func countLines(ctx context.Context, root *os.Root, path string, budget int64) (lines int, read int64, err error) {
	fi, err := root.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return 1, 0, nil
	}
	if !fi.Mode().IsRegular() {
		return 0, 0, fmt.Errorf("gitengine: not a regular file: %s", path)
	}
	need := min(fi.Size(), countBytesCap)
	if need > budget {
		return 0, 0, errCountBudget
	}
	f, err := rootfs.Open(root, path)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()
	var (
		buf    [32 * 1024]byte
		endsNL = true
	)
	for read < need {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		n, err := f.Read(buf[:min(int64(len(buf)), need-read)])
		if n > 0 {
			read += int64(n)
			lines += bytes.Count(buf[:n], []byte{'\n'})
			endsNL = buf[n-1] == '\n'
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, err
		}
	}
	if !endsNL {
		lines++
	}
	return lines, read, nil
}
