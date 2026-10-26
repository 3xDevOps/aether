package coordcli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// claudeWakeExit is the only exit code with which Claude Code's asyncRewake
// starts a turn, so the waiter uses it for a wake and for nothing else.
const claudeWakeExit = 2

// The waiter ends before Claude Code's hook timeout kills it.
const claudeWakeLifetime = time.Duration(coordhooks.ClaudeWakeTimeout)*time.Second - time.Minute

var claudeWakeBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

func claudeWakePath(run string) string {
	return filepath.Join(os.TempDir(), "aether-hook-"+run+"-claude-wake")
}

// claudeWokenPath records, as its mail, the IDs the waiter last started a
// turn for. Only the waiter writes it: a boundary hook's deferred write to
// the shared record could otherwise erase a wake recorded meanwhile.
func claudeWokenPath(run string) string {
	return filepath.Join(os.TempDir(), "aether-hook-"+run+"-claude-woken.json")
}

// claudeWake waits, for one idle Claude Code session, until the server admits
// a wake for mail no earlier wake started a turn for.
func claudeWake(ctx context.Context, cfg Config, input hookEvent) (code int, err error) {
	defer func() {
		if r := recover(); r != nil {
			code, err = ExitFailure, fmt.Errorf("hook claude wake: %v", r)
		}
	}()
	if input.AgentID != "" || (input.Name != "SessionStart" && input.Name != "Stop") {
		return ExitOK, nil
	}
	statusCtx, cancelStatus := context.WithTimeout(ctx, 3*time.Second)
	var status protocol.CoordStatusResult
	err = coordtransport.Call(statusCtx, cfg.Socket, protocol.MethodCoordHookStatus, nil, &status)
	cancelStatus()
	if err != nil {
		return ExitFailure, fmt.Errorf("hook claude wake: %w", err)
	}
	record := hookNoticePath(status.RunID, "claude")
	wokenRecord := claudeWokenPath(status.RunID)
	claim := claudeWakePath(status.RunID)
	pid := strconv.Itoa(os.Getpid())
	if err := claimClaudeWake(claim, pid); err != nil {
		return ExitFailure, fmt.Errorf("hook claude wake: claim the session: %w", err)
	}
	owns := func() bool {
		data, err := os.ReadFile(claim)
		return err == nil && string(data) == pid
	}
	// On SessionStart no unread mail has had a turn, so a resumed idle
	// session with mail waiting wakes at once. At Stop, mail a hook only
	// announced mid-turn still wakes.
	var woken []string

	ctx, cancel := context.WithTimeout(ctx, claudeWakeLifetime)
	defer cancel()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !owns() || hungUp(cfg.ErrOut) {
					cancel()
					return
				}
			}
		}
	}()

	var seen []string
	failures := 0
	for {
		wake, failure, err := requestHookWake(ctx, cfg.Socket, protocol.CoordHookStatusParams{
			SeenMessageIDs: seen, WaitSeconds: protocol.CoordMaxInboxWaitSeconds,
		})
		if ctx.Err() != nil {
			return ExitOK, nil
		}
		if err != nil {
			if failure != ExitFailure || failures == len(claudeWakeBackoff) {
				return ExitFailure, fmt.Errorf("hook claude wake: %w", err)
			}
			select {
			case <-ctx.Done():
				return ExitOK, nil
			case <-time.After(claudeWakeBackoff[failures]):
			}
			failures++
			continue
		}
		failures = 0
		ids := wake.UnreadMessageIDs
		if input.Name == "Stop" {
			woken = readHookNotices(wokenRecord).Mail
		}
		if wake.WakeAdmitted && hasNewMail(hookNotices{Unread: len(ids), Mail: ids}, woken) && owns() {
			writeHookNotices(wokenRecord, hookNotices{Mail: ids})
			notices := readHookNotices(record)
			notices.Mail = ids
			writeHookNotices(record, notices)
			if _, err := io.WriteString(cfg.ErrOut, protocol.CoordInboxContext(len(ids))+protocol.CoordReportReminder); err != nil {
				return ExitFailure, fmt.Errorf("hook claude wake: write the inbox instruction: %w", err)
			}
			return claudeWakeExit, nil
		}
		seen = ids
	}
}

func claimClaudeWake(path, pid string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(pid); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// hungUp reports that nothing reads w any more. Claude Code runs the hook
// under a shell that outlives a killed Claude Code, so the parent PID cannot
// tell; the closed peer of the hook's stderr can.
func hungUp(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	conn, err := f.SyscallConn()
	if err != nil {
		return false
	}
	gone := false
	_ = conn.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd)}}
		n, err := unix.Poll(fds, 0)
		gone = err == nil && n > 0 && fds[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0
	})
	return gone
}
