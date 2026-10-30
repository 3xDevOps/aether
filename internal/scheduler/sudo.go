package scheduler

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

// sudoTimeout is how long a sudo command waits for a person; the message
// that withdraws it says so.
const sudoTimeout = 10 * time.Minute

// ErrSudoNotPending is a decision on a sudo request that no longer waits.
var ErrSudoNotPending = errors.New("no such sudo request waits: it was decided, its command's caller went away, or it timed out")

// sudoRequest is one sudo command waiting for a person. DecideSudo sends
// decision when it removes the request from its run's set, both under
// Scheduler.mu, so a request missing from the set has its decision sent.
type sudoRequest struct {
	input    domain.RunInputRequest
	command  string
	decision chan sudoDecision
}

type sudoDecision struct {
	allow bool
	by    string
}

func sudoRefused(message string) error {
	return &protocol.Error{Code: protocol.CodeDenied, Message: message}
}

// Sudo runs a Default run's sudo command once a person allows it, or at once
// while the workspace setup script runs, and reports its exit code. A
// refusal is a *protocol.Error whose message the shim prints after "sudo: ".
func (s *Scheduler) Sudo(ctx context.Context, id domain.RunID, p protocol.RunSudoParams, stdin []byte, stdout, stderr io.Writer) (int, error) {
	rt, ok := s.cfg.Runtime.(runtime.SudoRuntime)
	if !ok {
		return 0, errors.New("scheduler: this runtime cannot run a sudo command")
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("scheduler: read run %s: %w", id, err)
	}
	switch run.Permission {
	case domain.PermissionIsolated:
		return 0, sudoRefused("an isolated run cannot run commands as root")
	case domain.PermissionRoot:
		return 0, sudoRefused("a root run's sudo is the image's own, at /usr/bin/sudo")
	}
	s.mu.Lock()
	entry := s.runs[id]
	var cid runtime.ID
	if entry != nil && !entry.status.Terminal() {
		cid = entry.containerID
	}
	s.mu.Unlock()
	if cid == "" {
		return 0, fmt.Errorf("%w: run %s", ErrNoLiveEnvironment, id)
	}
	if !rt.SettingUp(cid) {
		if p.NonInteractive {
			return 0, sudoRefused("a person must allow this command; -n cannot wait for one")
		}
		if err = s.awaitSudo(ctx, entry, p); err != nil {
			return 0, err
		}
	}
	code, err := rt.ExecAttached(ctx, cid, runtime.AttachedExec{
		Argv: p.Argv, WorkingDir: p.Dir, User: cmp.Or(p.User, "0:0"),
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		return 0, fmt.Errorf("scheduler: run the sudo command: %w", err)
	}
	return code, nil
}

// awaitSudo raises p as one of the run's pending requests and returns once a
// person allows it.
func (s *Scheduler) awaitSudo(ctx context.Context, entry *supervised, p protocol.RunSudoParams) error {
	quoted := make([]string, len(p.Argv))
	for i, arg := range p.Argv {
		quoted[i] = shellquote.Quote(arg)
	}
	as := "as " + shellquote.Quote(cmp.Or(p.User, "root")) + " in " + shellquote.Quote(p.Dir) + ": " + strings.Join(quoted, " ")
	req := &sudoRequest{
		input: domain.RunInputRequest{
			ID: domain.SudoRequestPrefix + strings.ToLower(rand.Text()), SessionID: domain.SudoSessionID,
			Kind: "permission", Title: "Run " + as,
		},
		command:  as,
		decision: make(chan sudoDecision, 1),
	}
	s.mu.Lock()
	if s.runs[entry.runID] != entry || entry.status.Terminal() {
		s.mu.Unlock()
		return fmt.Errorf("%w: run %s", ErrNoLiveEnvironment, entry.runID)
	}
	entry.sudo = append(entry.sudo, req)
	err := s.sudoChangedLocked(ctx, entry)
	s.mu.Unlock()
	if err != nil {
		s.withdrawSudo(ctx, entry, req)
		return fmt.Errorf("scheduler: show the sudo request: %w", err)
	}
	timer := time.NewTimer(s.sudoWait)
	defer timer.Stop()
	var d sudoDecision
	select {
	case d = <-req.decision:
	case <-ctx.Done():
		s.withdrawSudo(ctx, entry, req)
		return ctx.Err()
	case <-timer.C:
		var decided bool
		if d, decided = s.withdrawSudo(ctx, entry, req); !decided {
			return sudoRefused("nobody allowed this command within 10 minutes")
		}
	}
	if !d.allow {
		return sudoRefused(d.by + " denied this command")
	}
	return nil
}

// withdrawSudo takes req back unless a decision took it first, and then
// reports that decision.
func (s *Scheduler) withdrawSudo(ctx context.Context, entry *supervised, req *sudoRequest) (sudoDecision, bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.Index(entry.sudo, req)
	if i < 0 {
		return <-req.decision, true
	}
	entry.sudo = slices.Delete(entry.sudo, i, i+1)
	if err := s.sudoChangedLocked(ctx, entry); err != nil {
		slog.Warn("scheduler: publish a withdrawn sudo request", "run", entry.runID, "error", err)
	}
	return sudoDecision{}, false
}

// DecideSudo answers run's sudo request for member; the first decision wins.
// Whether member may allow it is the caller's to check.
func (s *Scheduler) DecideSudo(ctx context.Context, run domain.RunID, request string, allow bool, member domain.MemberID) error {
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return fmt.Errorf("scheduler: read member %s: %w", member, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil {
		return ErrSudoNotPending
	}
	i := slices.IndexFunc(entry.sudo, func(r *sudoRequest) bool { return r.input.ID == request })
	if i < 0 {
		return ErrSudoNotPending
	}
	req := entry.sudo[i]
	entry.sudo = slices.Delete(entry.sudo, i, i+1)
	req.decision <- sudoDecision{allow: allow, by: m.DisplayName}
	verb := "denied"
	if allow {
		verb = "allowed"
	}
	s.publishTimeline(ctx, entry.workspaceID, run, member, events.TimelineNote, verb+" sudo "+req.command)
	if err = s.sudoChangedLocked(ctx, entry); err != nil {
		return fmt.Errorf("scheduler: %s the sudo request, then could not publish the run's pending requests: %w", verb, err)
	}
	return nil
}

// sudoChangedLocked publishes the run's pending set and activity after its
// sudo requests changed. The caller must hold s.mu.
func (s *Scheduler) sudoChangedLocked(ctx context.Context, entry *supervised) error {
	if s.runs[entry.runID] != entry || entry.status.Terminal() {
		return nil
	}
	entry.inputPublishPending = true
	return errors.Join(s.refreshActivityLocked(ctx, entry), s.publishPendingInputLocked(ctx, entry))
}
