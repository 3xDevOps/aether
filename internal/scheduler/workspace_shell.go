package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// WorkspaceShell opens a server-owned interactive container for bootstrap or
// harness login. Disconnecting bootstrap preserves its pending staging state.
func (s *Scheduler) WorkspaceShell(ctx context.Context, member domain.MemberID, req domain.WorkspaceShellRequest, cols, rows uint, conn io.ReadWriter, resize <-chan [2]uint) error {
	if err := req.Validate(); err != nil {
		return err
	}
	slog.Info("workspace shell started", "mode", string(req.Mode))
	if err := s.validateWorkspaceShellMember(ctx, member); err != nil {
		return err
	}
	ws, err := s.resolveWorkspace(ctx, req.Workspace)
	if err != nil {
		return err
	}
	var profile harness.Profile
	if req.Mode == domain.WorkspaceShellHarnessLogin {
		_, profile, err = s.command(req.Harness, domain.LaunchTUI, "")
		if err != nil {
			return err
		}
	}
	var staging, pendingID string
	if req.Mode == domain.WorkspaceShellBootstrapTools {
		if s.cfg.Toolenv == nil {
			return errors.New("scheduler: tool environment is not configured")
		}
		pending, pendingErr := s.cfg.Store.ListPendingWorkspaceShells(ctx, member, ws.ID)
		if pendingErr != nil {
			return pendingErr
		}
		if req.Reset {
			if err := s.cfg.Toolenv.Reset(ctx, member, ws.ID); err != nil {
				return err
			}
			pending = nil
		}
		if req.Resume {
			switch len(pending) {
			case 0:
				return errors.New("scheduler: no pending bootstrap session to resume")
			case 1:
			default:
				return errors.New("scheduler: multiple pending bootstrap sessions; reset before resuming")
			}
			pendingID = pending[0].ID
			staging, err = s.cfg.Toolenv.StagingPath(member, ws.ID, pending[0].StagingID)
			if err != nil {
				return err
			}
		} else {
			staging, err = s.cfg.Toolenv.CreateStaging(string(member), string(ws.ID))
			if err != nil {
				return err
			}
			pending := &store.PendingWorkspaceShell{WorkspaceID: ws.ID, MemberID: member, StagingID: filepath.Base(staging)}
			if err := s.cfg.Store.CreatePendingWorkspaceShell(ctx, pending); err != nil {
				_ = s.cfg.Toolenv.CleanupStaging(staging)
				return err
			}
			pendingID = pending.ID
		}
	}
	plan, err := s.BuildEnvironmentPlan(ctx, nil, ws, &domain.Member{ID: member}, profile, purposeForShell(req.Mode), staging)
	if err != nil {
		return err
	}
	reservation, err := s.reserveCredentialUser(member, req.Harness, plan.User, len(profile.CredentialPaths) > 0 && req.Mode == domain.WorkspaceShellHarnessLogin, "workspace shell", nil)
	if err != nil {
		return err
	}
	defer s.releaseCredentialUser(reservation)
	spec := runtime.Spec{
		Name: "workspace-shell-" + string(member) + "-" + string(ws.ID), Image: plan.Image,
		Command: []string{"/bin/sh"}, TTY: true, Mounts: plan.Mounts, User: plan.User,
		Env: plan.Env, SetupScript: plan.SetupScript,
		CreationKey: fmt.Sprintf("workspace-shell-%s-%s-%d", member, ws.ID, time.Now().UnixNano()),
	}
	id, err := s.cfg.Runtime.Create(ctx, spec)
	if err != nil {
		return err
	}
	tornDown := false
	teardown := func() {
		if tornDown {
			return
		}
		tornDown = true
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.cfg.Runtime.Stop(cctx, id, 2*time.Second)
		_ = s.cfg.Runtime.Destroy(cctx, id)
	}
	defer teardown()
	if err := s.cfg.Runtime.Start(ctx, id); err != nil {
		return err
	}
	att, err := s.cfg.Runtime.Attach(ctx, id)
	if err != nil {
		return err
	}
	defer att.Close()
	if cols > 0 && rows > 0 {
		_ = att.Resize(ctx, cols, rows)
	}
	done := make(chan struct{})
	defer close(done)
	if resize != nil {
		go func() {
			for {
				select {
				case sz := <-resize:
					_ = att.Resize(ctx, sz[0], sz[1])
				case <-done:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	revoked := make(chan error, 1)
	monitorDone := make(chan struct{})
	defer close(monitorDone)
	interval := s.cfg.PollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.validateWorkspaceShellMember(ctx, member); err != nil {
					revoked <- err
					return
				}
			case <-monitorDone:

				return
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { _, _ = io.Copy(conn, att.Stdout()) }()
	inputDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(att.Stdin(), conn)
		inputDone <- copyErr
	}()
	waitDone := make(chan struct {
		status runtime.ExitStatus
		err    error
	}, 1)
	go func() {
		status, waitErr := s.cfg.Runtime.Wait(context.Background(), id)
		waitDone <- struct {
			status runtime.ExitStatus
			err    error
		}{status: status, err: waitErr}
	}()
	cleanExit := false
	var terminalErr error
	select {
	case result := <-waitDone:
		cleanExit = result.err == nil && result.status.Code == 0
		if result.err != nil {
			terminalErr = result.err
		}
	case <-ctx.Done():
		terminalErr = ctx.Err()
	case err := <-revoked:
		terminalErr = fmt.Errorf("scheduler: workspace shell access revoked: %w", err)
	case <-inputDone:
		if ctx.Err() != nil {
			terminalErr = ctx.Err()
			break
		}
		if memberErr := s.validateWorkspaceShellMember(ctx, member); memberErr != nil {
			terminalErr = fmt.Errorf("scheduler: workspace shell access revoked: %w", memberErr)
			break
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		select {
		case result := <-waitDone:
			cleanExit = result.err == nil && result.status.Code == 0
			if result.err != nil {
				terminalErr = result.err
			}
		case <-waitCtx.Done():
		}
		cancel()
	}
	if terminalErr != nil || !cleanExit {
		return terminalErr
	}
	if req.Mode == domain.WorkspaceShellBootstrapTools {
		manifest := domain.ToolManifest{}
		if req.VerificationExecutable != "" {
			executable := filepath.Join(staging, "bin", req.VerificationExecutable)
			info, statErr := os.Stat(executable)
			if statErr != nil || info.Mode().Perm()&0o111 == 0 {
				if statErr == nil {
					statErr = errors.New("executable is not executable")
				}
				slog.Warn("workspace shell verification failed", "mode", string(req.Mode))
				return fmt.Errorf("scheduler: verify bootstrap executable: %w", statErr)
			}
			manifest.Executable = req.VerificationExecutable
		}
		slog.Info("workspace shell promoted", "mode", string(req.Mode))
		if _, err := s.cfg.Toolenv.Promote(ctx, string(member), string(ws.ID), staging, manifest, nil); err != nil {
			return err
		}
		if pendingID != "" {
			if err := s.cfg.Store.DeletePendingWorkspaceShell(ctx, pendingID); err != nil {
				return fmt.Errorf("scheduler: delete pending bootstrap session: %w", err)
			}
		}
	}
	return nil
}
func (s *Scheduler) validateWorkspaceShellMember(ctx context.Context, member domain.MemberID) error {
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return fmt.Errorf("scheduler: workspace shell member: %w", err)
	}
	if m.Pending {
		return errors.New("scheduler: workspace shell member approval was revoked")
	}
	if err := permissions.Check(permissions.Launch, permissions.Actor{ID: m.ID, Role: m.Role}, permissions.Target{}); err != nil {
		return fmt.Errorf("scheduler: workspace shell permission revoked: %w", err)
	}
	return nil
}

func purposeForShell(mode domain.WorkspaceShellMode) EnvironmentPurpose {
	if mode == domain.WorkspaceShellBootstrapTools {
		return EnvironmentPurposeBootstrap
	}
	return EnvironmentPurposeLogin
}

func (s *Scheduler) resolveWorkspace(ctx context.Context, selector domain.WorkspaceSelector) (*domain.Workspace, error) {
	if selector.ID != "" {
		return s.cfg.Store.GetWorkspace(ctx, selector.ID)
	}
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	for _, ws := range workspaces {
		if ws.Name == selector.Name {
			return ws, nil
		}
	}
	return nil, store.ErrNotFound
}
