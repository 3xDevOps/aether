package scheduler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// DevelopmentPTYHost is optional so a runtime without owned executions cannot
// accidentally expose ordinary, unmanaged ExecTTY as a development terminal.
type DevelopmentPTYHost interface {
	StartDevelopmentSession(context.Context, ptyhost.SessionKey, runtime.Attachment, uint, uint) error
	ObserveSession(ptyhost.SessionKey) (ptyhost.ScreenObservation, error)
	SessionGeometry(ptyhost.SessionKey) (uint, uint, uint64, error)
	ReadSessionOutput(ptyhost.SessionKey, ptyhost.OutputRequest) (ptyhost.OutputObservation, error)
	WaitSession(context.Context, ptyhost.SessionKey, ptyhost.WaitRequest) (ptyhost.WaitObservation, error)
	WriteSessionInput(context.Context, ptyhost.SessionKey, ptyhost.SessionAdmission, []byte) error
	ResizeSession(context.Context, ptyhost.SessionKey, ptyhost.SessionAdmission, uint, uint) error
	Attach(context.Context, ptyhost.SessionKey, ptyhost.AttachClient, io.ReadWriter, <-chan [2]uint) error
}

// All fields beneath a set are protected by the existing per-run shell lock.
// Only complete immutable ownership identities are written to disk.
type runTerminalSet struct {
	Version     int                     `json:"version"`
	RunID       domain.RunID            `json:"run_id"`
	ContainerID runtime.ID              `json:"container_id"`
	Closed      bool                    `json:"closed"`
	Terminals   map[string]*runTerminal `json:"terminals"`
}

type runTerminal struct {
	ID             string               `json:"id"`
	Incarnation    string               `json:"incarnation"`
	Identity       runtime.ExecIdentity `json:"identity"`
	Cols           uint                 `json:"cols"`
	Rows           uint                 `json:"rows"`
	State          runtime.ExecState    `json:"state"`
	Stopped        bool                 `json:"stopped"`
	ContainerEnded bool                 `json:"container_ended,omitempty"`
	Unavailable    string               `json:"unavailable,omitempty"`
	Generation     uint64               `json:"-"`
	Exec           runtime.ManagedExec  `json:"-"`
}

func (s *Scheduler) DevelopmentTerminalsAvailable() error {
	if _, ok := s.cfg.Runtime.(runtime.ManagedExecRuntime); !ok {
		return fmt.Errorf("%w: runtime does not support owned terminal executions", runtime.ErrExecUnavailable)
	}
	if _, ok := s.cfg.PTY.(DevelopmentPTYHost); !ok {
		return fmt.Errorf("%w: terminal observation and fenced input are unavailable", runtime.ErrExecUnavailable)
	}
	return nil
}

func (s *Scheduler) runTerminalsPath(run domain.RunID) string {
	key := sha256.Sum256([]byte(run))
	return filepath.Join(s.cfg.StateDir, "run-terminals", hex.EncodeToString(key[:])+".json")
}

func (s *Scheduler) persistRunTerminalsLocked(run domain.RunID, set *runTerminalSet) error {
	data, err := json.Marshal(set)
	if err != nil {
		return err
	}
	path := s.runTerminalsPath(run)
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".terminal-*")
	if err != nil {
		return err
	}
	_, writeErr := tmp.Write(data)
	if writeErr == nil {
		writeErr = tmp.Sync()
	}
	closeErr := tmp.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr == nil {
		writeErr = os.Rename(tmp.Name(), path)
	}
	if writeErr == nil {
		writeErr = fsyncDir(dir)
	}
	if removeErr := os.Remove(tmp.Name()); removeErr != nil && !os.IsNotExist(removeErr) {
		writeErr = errors.Join(writeErr, removeErr)
	}
	return writeErr
}

func (s *Scheduler) loadRunTerminalsLocked(ctx context.Context, run domain.RunID) (*runTerminalSet, error) {
	s.mu.Lock()
	set := s.runShellTerminals[run]
	s.mu.Unlock()
	if set != nil {
		return set, nil
	}
	set = &runTerminalSet{Version: 1, RunID: run, Terminals: make(map[string]*runTerminal)}
	file, err := os.Open(s.runTerminalsPath(run))
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		_ = file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(data) > 64<<10 {
			return nil, fmt.Errorf("terminal registry exceeds limit")
		}
		if err = json.Unmarshal(data, set); err != nil {
			return nil, fmt.Errorf("decode terminal registry: %w", err)
		}
		if set.Version != 1 || set.RunID != run || len(set.Terminals) > 4 || set.Terminals == nil {
			return nil, fmt.Errorf("invalid terminal registry")
		}
		for id, terminal := range set.Terminals {
			if terminal == nil || !runShellTabName.MatchString(id) || terminal.ID != id || terminal.Incarnation == "" ||
				terminal.Identity.ContainerID == "" || terminal.Identity.ExecID == "" || terminal.Identity.CreationKey == "" || terminal.Identity.ClaimToken == "" {
				return nil, fmt.Errorf("invalid persisted terminal identity")
			}
			if terminal.ContainerEnded {
				continue
			}
			managed, ok := s.cfg.Runtime.(runtime.ManagedExecRuntime)
			if !ok {
				terminal.Unavailable = "runtime does not support execution recovery"
				continue
			}
			terminal.Exec, err = managed.RecoverExec(ctx, terminal.Identity)
			if err != nil {
				terminal.Unavailable = err.Error()
				continue
			}
			state, statusErr := terminal.Exec.Status(ctx)
			if statusErr != nil {
				terminal.Unavailable = statusErr.Error()
			} else {
				terminal.State = state
				terminal.Unavailable = "PTY unavailable after recovery; execution was not reattached or restarted"
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	s.mu.Lock()
	if s.runShellTerminals == nil {
		s.runShellTerminals = make(map[domain.RunID]*runTerminalSet)
	}
	s.runShellTerminals[run] = set
	s.mu.Unlock()
	return set, nil
}

func (s *Scheduler) prepareRunTerminalsLocked(set *runTerminalSet, container runtime.ID) error {
	if set.ContainerID != "" && set.ContainerID != container {
		// Relaunch is a new container, not proof that old owned processes died.
		for _, terminal := range set.Terminals {
			if !terminal.State.Exited {
				return fmt.Errorf("%w: previous container terminal requires explicit stop", runtime.ErrExecUnavailable)
			}
		}
		set.Closed = false
	}
	if set.Closed {
		return fmt.Errorf("%w: run terminal admissions are closed", ptyhost.ErrNoSession)
	}
	set.ContainerID = container
	return nil
}

func (s *Scheduler) refreshRunTerminalLocked(ctx context.Context, run domain.RunID, set *runTerminalSet, terminal *runTerminal) error {
	changed, attached := false, false
	if terminal.Generation != 0 {
		if host, ok := s.cfg.PTY.(DevelopmentPTYHost); ok {
			cols, rows, generation, err := host.SessionGeometry(ptyhost.RunShellSession(run, terminal.ID))
			if err == nil && generation == terminal.Generation {
				changed = cols != terminal.Cols || rows != terminal.Rows
				terminal.Cols, terminal.Rows, attached = cols, rows, true
			} else {
				terminal.Unavailable = "terminal PTY is unavailable or replaced"
			}
		}
	}
	if terminal.Exec != nil && !terminal.ContainerEnded {
		state, err := terminal.Exec.Status(ctx)
		if err != nil {
			terminal.Unavailable = err.Error()
		} else {
			changed = changed || state.Running != terminal.State.Running || state.Exited != terminal.State.Exited ||
				(state.ExitCode != nil && (terminal.State.ExitCode == nil || *state.ExitCode != *terminal.State.ExitCode))
			terminal.State = state
			if attached {
				terminal.Unavailable = ""
				if state.Running && !state.Attached {
					terminal.Unavailable = state.UnavailableReason
				}
			}
		}
	}
	if changed {
		return s.persistRunTerminalsLocked(run, set)
	}
	return nil
}

func terminalDescription(terminal *runTerminal) protocol.DevTerminal {
	process := protocol.DevProcessState{State: "unavailable", Reason: terminal.Unavailable}
	switch {
	case terminal.State.Exited && terminal.State.ExitCode != nil:
		code := *terminal.State.ExitCode
		process.State, process.ExitCode = "exited", &code
		if terminal.Stopped {
			process.State = "stopped"
		}
	case terminal.ContainerEnded:
		process.State = "exited"
	case terminal.Unavailable == "" && terminal.State.Running:
		process.State = "running"
	}
	return protocol.DevTerminal{TerminalID: terminal.ID, Incarnation: terminal.Incarnation, Name: terminal.ID,
		Cols: terminal.Cols, Rows: terminal.Rows, Process: process}
}

func (s *Scheduler) startRunTerminalLocked(ctx context.Context, run domain.RunID, set *runTerminalSet, live LiveRun, params protocol.DevTerminalStartParams) (*runTerminal, error) {
	if err := s.DevelopmentTerminalsAvailable(); err != nil {
		return nil, err
	}
	if params.Name == "" {
		for n := 1; n <= 4; n++ {
			candidate := fmt.Sprintf("tab-%d", n)
			if set.Terminals[candidate] == nil {
				params.Name = candidate
				break
			}
		}
		if params.Name == "" {
			return nil, ErrRunShellTabLimit
		}
	}
	if !runShellTabName.MatchString(params.Name) {
		return nil, ErrInvalidRunShellTab
	}
	if params.Cols == 0 {
		params.Cols = 80
	}
	if params.Rows == 0 {
		params.Rows = 24
	}
	if err := terminalDimensions(params.Cols, params.Rows); err != nil {
		return nil, err
	}
	old := set.Terminals[params.Name]
	if old != nil {
		if err := s.refreshRunTerminalLocked(ctx, run, set, old); err != nil {
			return nil, err
		}
		if !old.State.Exited || (old.State.ExitCode == nil && !old.ContainerEnded) {
			return nil, fmt.Errorf("terminal %q already exists; stop its exact incarnation before replacing it", params.Name)
		}
	} else if len(set.Terminals) >= 4 {
		return nil, ErrRunShellTabLimit
	}
	argv := params.Command
	if len(argv) == 0 {
		argv = []string{"/bin/bash", "-l"}
	}
	if len(argv) > 256 || argv[0] == "" {
		return nil, fmt.Errorf("invalid terminal command")
	}
	commandBytes := 0
	for _, arg := range argv {
		commandBytes += len(arg)
		if strings.ContainsRune(arg, 0) {
			return nil, fmt.Errorf("terminal command contains NUL")
		}
	}
	if commandBytes > protocol.MaxDevInputBytes {
		return nil, fmt.Errorf("terminal command exceeds limit")
	}
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	incarnation := hex.EncodeToString(token[:])
	managed := s.cfg.Runtime.(runtime.ManagedExecRuntime)
	execution, err := managed.StartExecTTY(ctx, live.ContainerID, runtime.ExecSpec{Argv: argv, WorkingDir: live.Workdir,
		Cols: params.Cols, Rows: params.Rows, CreationKey: "terminal-" + incarnation})
	var exitErr *runtime.ExecExitError
	if len(params.Command) == 0 && errors.As(err, &exitErr) && (exitErr.Code == 126 || exitErr.Code == 127) {
		execution, err = managed.StartExecTTY(ctx, live.ContainerID, runtime.ExecSpec{Argv: []string{"/bin/sh", "-l"}, WorkingDir: live.Workdir,
			Cols: params.Cols, Rows: params.Rows, CreationKey: "terminal-" + incarnation + "-sh"})
	}
	if err != nil {
		return nil, fmt.Errorf("start owned terminal: %w", err)
	}
	terminal := &runTerminal{ID: params.Name, Incarnation: incarnation, Identity: execution.Identity(), Exec: execution,
		Cols: params.Cols, Rows: params.Rows, State: runtime.ExecState{Running: true, Attached: true}}
	if terminal.Identity.ContainerID == "" || terminal.Identity.ExecID == "" || terminal.Identity.CreationKey == "" || terminal.Identity.ClaimToken == "" {
		return nil, errors.Join(fmt.Errorf("runtime returned incomplete terminal ownership"), cleanupUnpublishedTerminal(execution))
	}
	set.Terminals[params.Name] = terminal
	if err = s.persistRunTerminalsLocked(run, set); err != nil {
		if old == nil {
			delete(set.Terminals, params.Name)
		} else {
			set.Terminals[params.Name] = old
		}
		return nil, errors.Join(err, cleanupUnpublishedTerminal(execution))
	}
	key := ptyhost.RunShellSession(run, terminal.ID)
	s.runShellReservationMu.Lock()
	delete(s.runShellReservations, string(key))
	s.runShellReservationMu.Unlock()
	if old != nil {
		_ = s.cfg.PTY.StopSession(ctx, key)
		if old.Exec != nil {
			_ = old.Exec.Detach()
		}
	}
	host := s.cfg.PTY.(DevelopmentPTYHost)
	if execution.Attachment() == nil {
		err = runtime.ErrExecUnavailable
	} else {
		err = host.StartDevelopmentSession(ctx, key, execution.Attachment(), params.Cols, params.Rows)
	}
	if err == nil {
		terminal.Generation = s.cfg.PTY.SessionGeneration(key)
		if terminal.Generation == 0 {
			err = ptyhost.ErrSessionReplaced
		}
	}
	if err != nil {
		terminal.Unavailable = err.Error()
		cleanupErr := cleanupUnpublishedTerminal(execution)
		_ = s.refreshRunTerminalLocked(context.WithoutCancel(ctx), run, set, terminal)
		return nil, errors.Join(err, cleanupErr, s.persistRunTerminalsLocked(run, set))
	}
	return terminal, nil
}

func cleanupUnpublishedTerminal(execution runtime.ManagedExec) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := execution.Stop(ctx, 0)
	return errors.Join(err, execution.Detach())
}

func (s *Scheduler) stopRunTerminalLocked(ctx context.Context, run domain.RunID, set *runTerminalSet, terminal *runTerminal, grace time.Duration) error {
	if terminal.ContainerEnded || (terminal.State.Exited && terminal.State.ExitCode != nil) {
		return nil
	}
	if terminal.Exec == nil {
		return fmt.Errorf("%w: %s", runtime.ErrExecUnavailable, terminal.Unavailable)
	}
	exit, err := terminal.Exec.Stop(ctx, grace)
	if err != nil {
		terminal.Unavailable = err.Error()
		return errors.Join(err, s.persistRunTerminalsLocked(run, set))
	}
	terminal.State = runtime.ExecState{Exited: true, ExitCode: &exit.Code}
	terminal.Stopped = true
	terminal.Unavailable = ""
	// Keep the ended PTY and its rendered/output observations until explicit
	// replacement or run deletion. Closing an attachment is not process stop.
	return s.persistRunTerminalsLocked(run, set)
}

func (s *Scheduler) RecoverDevelopmentTerminals(ctx context.Context, run domain.RunID) error {
	lock := s.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	_, err := s.loadRunTerminalsLocked(ctx, run)
	return err
}

func (s *Scheduler) StopDevelopmentTerminals(ctx context.Context, run domain.RunID) error {
	lock := s.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	set, err := s.loadRunTerminalsLocked(ctx, run)
	if err != nil {
		return err
	}
	set.Closed = true
	var errs []error
	for _, terminal := range set.Terminals {
		if err := s.stopRunTerminalLocked(ctx, run, set, terminal, s.cfg.StopGrace); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(append(errs, s.persistRunTerminalsLocked(run, set))...)
}

// MarkDevelopmentContainerEnded requires a proved parent-container exit or
// destruction. It never invents the independently owned command's exit code.
func (s *Scheduler) MarkDevelopmentContainerEnded(ctx context.Context, run domain.RunID) error {
	lock := s.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	set, err := s.loadRunTerminalsLocked(ctx, run)
	if err != nil {
		return err
	}
	set.Closed = true
	for _, terminal := range set.Terminals {
		terminal.ContainerEnded = true
		if !terminal.State.Exited || terminal.State.ExitCode == nil {
			terminal.State = runtime.ExecState{Exited: true}
			terminal.Unavailable = "run container exited; command exit status unavailable"
		}
	}
	return s.persistRunTerminalsLocked(run, set)
}

// ReopenDevelopmentTerminals is called only at a successful relaunch boundary.
// It keeps exited observations, but never restores a prior writer or command.
func (s *Scheduler) ReopenDevelopmentTerminals(ctx context.Context, run domain.RunID) error {
	lock := s.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	set, err := s.loadRunTerminalsLocked(ctx, run)
	if err != nil {
		return err
	}
	for _, terminal := range set.Terminals {
		if err := s.refreshRunTerminalLocked(ctx, run, set, terminal); err != nil {
			return err
		}
		if !terminal.State.Exited || (terminal.State.ExitCode == nil && !terminal.ContainerEnded) {
			return fmt.Errorf("%w: prior terminal has not proved exit", runtime.ErrExecUnavailable)
		}
	}
	set.Closed = false
	return s.persistRunTerminalsLocked(run, set)
}

func (s *Scheduler) DetachDevelopmentTerminals(ctx context.Context) error {
	s.mu.Lock()
	runs := make([]domain.RunID, 0, len(s.runShellTerminals))
	for run := range s.runShellTerminals {
		runs = append(runs, run)
	}
	s.mu.Unlock()
	var errs []error
	for _, run := range runs {
		lock := s.lockForShell(run)
		lock.Lock()
		set, err := s.loadRunTerminalsLocked(ctx, run)
		if err == nil {
			for _, terminal := range set.Terminals {
				if terminal.Exec != nil {
					errs = append(errs, terminal.Exec.Detach())
				}
				terminal.Generation = 0
				terminal.Unavailable = "PTY detached on server shutdown; command was not stopped"
			}
			err = s.persistRunTerminalsLocked(run, set)
		}
		errs = append(errs, err)
		lock.Unlock()
	}
	return errors.Join(errs...)
}

func (s *Scheduler) DeleteDevelopmentTerminals(ctx context.Context, run domain.RunID) error {
	if err := s.StopDevelopmentTerminals(ctx, run); err != nil {
		return err
	}
	lock := s.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	set, err := s.loadRunTerminalsLocked(ctx, run)
	if err != nil {
		return err
	}
	for _, terminal := range set.Terminals {
		_ = s.cfg.PTY.StopSession(ctx, ptyhost.RunShellSession(run, terminal.ID))
		if terminal.Exec != nil {
			_ = terminal.Exec.Detach()
		}
	}
	if err := os.Remove(s.runTerminalsPath(run)); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Keep a closed in-memory tombstone until the scheduler itself ends.
	set.Terminals = make(map[string]*runTerminal)
	return nil
}

func (s *Scheduler) LookupDevelopmentTerminal(ctx context.Context, run domain.RunID, terminalID string) (protocol.DevTerminal, error) {
	lock := s.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	set, err := s.loadRunTerminalsLocked(ctx, run)
	if err != nil {
		return protocol.DevTerminal{}, err
	}
	terminal := set.Terminals[terminalID]
	if terminal == nil {
		return protocol.DevTerminal{}, ptyhost.ErrNoSession
	}
	if err := s.refreshRunTerminalLocked(ctx, run, set, terminal); err != nil {
		return protocol.DevTerminal{}, err
	}
	return terminalDescription(terminal), nil
}

func terminalTarget(set *runTerminalSet, target protocol.DevTerminalTarget) (*runTerminal, error) {
	terminal := set.Terminals[target.TerminalID]
	if terminal == nil {
		return nil, ptyhost.ErrNoSession
	}
	if target.Incarnation == "" || target.Incarnation != terminal.Incarnation {
		return nil, ptyhost.ErrSessionReplaced
	}
	return terminal, nil
}

func (s *Scheduler) CaptureDevelopmentTerminal(ctx context.Context, run domain.RunID, target protocol.DevTerminalTarget) (protocol.DevTerminal, ptyhost.ScreenObservation, error) {
	lock := s.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	set, err := s.loadRunTerminalsLocked(ctx, run)
	if err != nil {
		return protocol.DevTerminal{}, ptyhost.ScreenObservation{}, err
	}
	terminal, err := terminalTarget(set, target)
	if err != nil {
		return protocol.DevTerminal{}, ptyhost.ScreenObservation{}, err
	}
	if err = s.refreshRunTerminalLocked(ctx, run, set, terminal); err != nil {
		return protocol.DevTerminal{}, ptyhost.ScreenObservation{}, err
	}
	description := terminalDescription(terminal)
	if terminal.Generation == 0 {
		return description, ptyhost.ScreenObservation{}, fmt.Errorf("%w: %s", runtime.ErrExecUnavailable, terminal.Unavailable)
	}
	host, ok := s.cfg.PTY.(DevelopmentPTYHost)
	if !ok {
		return description, ptyhost.ScreenObservation{}, runtime.ErrExecUnavailable
	}
	observation, err := host.ObserveSession(ptyhost.RunShellSession(run, terminal.ID))
	if err == nil && observation.Generation != terminal.Generation {
		err = ptyhost.ErrSessionReplaced
	}
	if err == nil {
		description.Cols, description.Rows = observation.Cols, observation.Rows
	}
	return description, observation, err
}

func (s *Scheduler) DevelopmentTerminalAdmission(ctx context.Context, run domain.RunID, principal control.Principal, target protocol.DevTerminalTarget, fence protocol.DevControlFence, authorize func() error) (ptyhost.SessionAdmission, error) {
	if s.cfg.Control == nil || authorize == nil {
		return ptyhost.SessionAdmission{}, control.ErrInvalid
	}
	lock := s.lockForShell(run)
	lock.Lock()
	set, err := s.loadRunTerminalsLocked(ctx, run)
	var terminal *runTerminal
	if err == nil {
		terminal, err = terminalTarget(set, target)
	}
	if err != nil {
		lock.Unlock()
		return ptyhost.SessionAdmission{}, err
	}
	generation := terminal.Generation
	lock.Unlock()
	if generation == 0 {
		return ptyhost.SessionAdmission{}, runtime.ErrExecUnavailable
	}
	surface := control.Surface{Kind: control.SurfaceTerminal, ID: target.TerminalID, Incarnation: target.Incarnation}
	return ptyhost.SessionAdmission{Generation: generation, Member: principal.MemberID, Admit: func(accept func() error) error {
		return s.cfg.Control.AdmitSurface(string(run), surface, principal, fence.ControlSessionID, fence.ControlGeneration, func() error {
			lock.Lock()
			defer lock.Unlock()
			current, err := terminalTarget(set, target)
			if err != nil {
				return err
			}
			if set.Closed || current.Generation != generation {
				return ptyhost.ErrSessionReplaced
			}
			if current.State.Exited {
				return ptyhost.ErrSessionEnded
			}
			live, err := s.ResolveLiveRun(ctx, run, false)
			if err != nil {
				return err
			}
			if current.Identity.ContainerID != live.ContainerID {
				return ptyhost.ErrSessionReplaced
			}
			if err := authorize(); err != nil {
				return err
			}
			return accept()
		})
	}}, nil
}

func (s *Scheduler) AttachDevelopmentTerminal(ctx context.Context, run domain.RunID, principal control.Principal, target protocol.DevTerminalTarget, fence protocol.DevControlFence, client ptyhost.AttachClient, conn io.ReadWriter, resize <-chan [2]uint, authorize func() error) error {
	if authorize == nil {
		return control.ErrInvalid
	}
	if err := authorize(); err != nil {
		return err
	}
	_, observation, err := s.CaptureDevelopmentTerminal(ctx, run, target)
	if err != nil {
		return err
	}
	client.SessionGeneration, client.Member = observation.Generation, principal.MemberID
	if !client.ReadOnly {
		admission, err := s.DevelopmentTerminalAdmission(ctx, run, principal, target, fence, authorize)
		if err != nil {
			return err
		}
		client.InputAdmission = admission.Admit
		previousAuthorize := client.Authorize
		client.Authorize = func() error {
			if err := authorize(); err != nil {
				return err
			}
			if previousAuthorize != nil {
				return previousAuthorize()
			}
			return nil
		}
	}
	return s.cfg.PTY.(DevelopmentPTYHost).Attach(ctx, ptyhost.RunShellSession(run, target.TerminalID), client, conn, resize)
}

func (s *Scheduler) DevelopmentTerminal(ctx context.Context, run domain.RunID, principal control.Principal, method string, raw json.RawMessage, authorize func() error) (any, error) {
	if authorize == nil {
		return nil, control.ErrInvalid
	}
	switch principal.Kind {
	case control.PrincipalRunAgent:
		if principal.RunID != run || principal.RunID == "" || principal.MemberID != "" {
			return nil, control.ErrInvalid
		}
	case control.PrincipalMember:
		if principal.MemberID == "" || principal.RunID != "" {
			return nil, control.ErrInvalid
		}
	default:
		return nil, control.ErrInvalid
	}
	if err := authorize(); err != nil {
		return nil, err
	}
	switch method {
	case protocol.MethodDevTerminalList:
		var params protocol.DevTerminalListParams
		if err := decodeTerminalParams(raw, &params); err != nil {
			return nil, err
		}
		lock := s.lockForShell(run)
		lock.Lock()
		defer lock.Unlock()
		set, err := s.loadRunTerminalsLocked(ctx, run)
		if err != nil {
			return nil, err
		}
		result := protocol.DevTerminalListResult{Terminals: make([]protocol.DevTerminal, 0, len(set.Terminals))}
		for _, terminal := range set.Terminals {
			if err := s.refreshRunTerminalLocked(ctx, run, set, terminal); err != nil {
				return nil, err
			}
			result.Terminals = append(result.Terminals, terminalDescription(terminal))
		}
		sort.Slice(result.Terminals, func(i, j int) bool { return result.Terminals[i].TerminalID < result.Terminals[j].TerminalID })
		return result, nil
	case protocol.MethodDevTerminalStart:
		var params protocol.DevTerminalStartParams
		if err := decodeTerminalParams(raw, &params); err != nil {
			return nil, err
		}
		lock := s.lockForShell(run)
		lock.Lock()
		defer lock.Unlock()
		live, err := s.ResolveLiveRun(ctx, run, false)
		if err != nil {
			return nil, err
		}
		set, err := s.loadRunTerminalsLocked(ctx, run)
		if err != nil {
			return nil, err
		}
		if err = s.prepareRunTerminalsLocked(set, live.ContainerID); err != nil {
			return nil, err
		}
		if err = authorize(); err != nil {
			return nil, err
		}
		terminal, err := s.startRunTerminalLocked(ctx, run, set, live, params)
		if err != nil {
			return nil, err
		}
		return protocol.DevTerminalStartResult{Terminal: terminalDescription(terminal)}, nil
	case protocol.MethodDevTerminalOutput:
		var params protocol.DevTerminalOutputParams
		if err := decodeTerminalParams(raw, &params); err != nil {
			return nil, err
		}
		return s.developmentTerminalOutput(ctx, run, params)
	case protocol.MethodDevTerminalScreen:
		var params protocol.DevTerminalScreenParams
		if err := decodeTerminalParams(raw, &params); err != nil {
			return nil, err
		}
		terminal, observation, err := s.CaptureDevelopmentTerminal(ctx, run, params.DevTerminalTarget)
		if err != nil {
			return nil, err
		}
		return protocol.PageDevTerminalScreen(terminalScreen(terminal, observation), params)
	case protocol.MethodDevTerminalInput:
		var params protocol.DevTerminalInputParams
		if err := decodeTerminalParams(raw, &params); err != nil {
			return nil, err
		}
		return s.developmentTerminalInput(ctx, run, principal, params, authorize)
	case protocol.MethodDevTerminalResize:
		var params protocol.DevTerminalResizeParams
		if err := decodeTerminalParams(raw, &params); err != nil {
			return nil, err
		}
		return s.developmentTerminalResize(ctx, run, principal, params, authorize)
	case protocol.MethodDevTerminalWait:
		var params protocol.DevTerminalWaitParams
		if err := decodeTerminalParams(raw, &params); err != nil {
			return nil, err
		}
		return s.developmentTerminalWait(ctx, run, params, authorize)
	case protocol.MethodDevTerminalStop:
		var params protocol.DevTerminalStopParams
		if err := decodeTerminalParams(raw, &params); err != nil {
			return nil, err
		}
		return s.developmentTerminalStop(ctx, run, principal, params, authorize)
	default:
		return nil, fmt.Errorf("unsupported terminal method %q", method)
	}
}

func decodeTerminalParams(raw json.RawMessage, params any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > protocol.MaxDevParamsBytes {
		return fmt.Errorf("terminal parameters exceed limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(params); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("terminal parameters contain trailing data")
	}
	return nil
}
