package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	switch req.Mode {
	case domain.WorkspaceShellHarnessLogin:
		_, profile, err = s.command(ctx, member, req.Harness, domain.LaunchTUI, "")
		if err != nil {
			return err
		}
	case domain.WorkspaceShellAgentSetup:
		profile, err = s.agentSetupProfile(req)
		if err != nil {
			return err
		}
		release, acquireErr := s.acquireAgentSetup(member, req.Harness)
		if acquireErr != nil {
			return acquireErr
		}
		defer release()
	}
	needsStaging := req.Mode == domain.WorkspaceShellBootstrapTools || req.Mode == domain.WorkspaceShellAgentSetup
	var staging, pendingID string
	if needsStaging {
		if s.cfg.Toolenv == nil {
			return errors.New("scheduler: tool environment is not configured")
		}
		pending, pendingErr := s.cfg.Store.ListPendingWorkspaceShells(ctx, member, ws.ID)
		if pendingErr != nil {
			return pendingErr
		}
		if req.Reset {
			if resetErr := s.cfg.Toolenv.Reset(ctx, member, ws.ID); resetErr != nil {
				return resetErr
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
			// Seed the fresh staging with the active snapshot so promotion
			// accumulates tools: adding agent B must not evict agent A from
			// the single active head this member+workspace has.
			if seedErr := s.seedStagingFromActiveSnapshot(ctx, member, ws.ID, staging); seedErr != nil {
				_ = s.cfg.Toolenv.CleanupStaging(staging)
				return seedErr
			}
			pendingRow := &store.PendingWorkspaceShell{WorkspaceID: ws.ID, MemberID: member, StagingID: filepath.Base(staging)}
			if createErr := s.cfg.Store.CreatePendingWorkspaceShell(ctx, pendingRow); createErr != nil {
				_ = s.cfg.Toolenv.CleanupStaging(staging)
				return createErr
			}
			pendingID = pendingRow.ID
		}
	}
	plan, err := s.BuildEnvironmentPlan(ctx, nil, ws, &domain.Member{ID: member}, profile, purposeForShell(req.Mode), staging)
	if err != nil {
		return err
	}
	if req.Mode == domain.WorkspaceShellAgentSetup && len(plan.Mounts) < 2 {
		return errors.New("scheduler: agent setup plan is missing its home and staging mounts")
	}
	// Writable shell mounts (staging, harness home) are server-created with
	// server ownership; a non-root image user could not write them without
	// the same ownership pass runs get.
	if ownErr := s.applyRunOwnership(ws, &domain.Run{}, plan.Mounts, plan.User); ownErr != nil {
		return fmt.Errorf("scheduler: workspace shell ownership: %w", ownErr)
	}
	if _, ok := plan.Env["PS1"]; !ok {
		switch req.Mode {
		case domain.WorkspaceShellBootstrapTools:
			plan.Env["PS1"] = "aether-bootstrap$ "
		case domain.WorkspaceShellAgentSetup:
			plan.Env["PS1"] = "aether-agent-setup$ "
		default:
			plan.Env["PS1"] = "aether-setup$ "
		}
	}
	sharedHome := req.Mode == domain.WorkspaceShellAgentSetup ||
		(len(profile.CredentialPaths) > 0 && req.Mode == domain.WorkspaceShellHarnessLogin)
	reservation, err := s.reserveCredentialUser(member, req.Harness, plan.User, sharedHome, "workspace shell", nil)
	if err != nil {
		return err
	}
	defer s.releaseCredentialUser(reservation)
	// Provisioning can include an image pull; tell the member the session
	// is alive before the potentially slow container creation.
	_, _ = io.WriteString(conn, "aether: provisioning workspace shell container...\r\n")
	shellCommand := []string{"/bin/sh", "-i"}
	if req.Mode == domain.WorkspaceShellAgentSetup {
		shellCommand = agentSetupCommand(req.Harness, profile.InstallScript, stagedExecutable(req, profile))
	}
	spec := runtime.Spec{
		Name: "workspace-shell-" + string(member) + "-" + string(ws.ID), Image: plan.Image,
		Command: shellCommand, TTY: true, Mounts: plan.Mounts, User: plan.User,
		Env: plan.Env, SetupScript: plan.SetupScript, WorkingDir: plan.Home,
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
	// Attach before Start: Docker attach only streams output produced after
	// the attach connects, so attaching late would drop the shell's first
	// prompt and greet the member with a blank screen.
	att, err := s.cfg.Runtime.Attach(ctx, id)
	if err != nil {
		return err
	}
	defer func() { _ = att.Close() }()
	if startErr := s.cfg.Runtime.Start(ctx, id); startErr != nil {
		return startErr
	}
	if cols > 0 && rows > 0 {
		_ = att.Resize(ctx, cols, rows)
	}
	switch req.Mode {
	case domain.WorkspaceShellBootstrapTools:
		_, _ = io.WriteString(conn, "aether: bootstrap shell ready. Install tools into ~/.local/bin; exit cleanly to snapshot them.\r\n")
	case domain.WorkspaceShellAgentSetup:
		_, _ = io.WriteString(conn, "aether: agent setup shell ready. Install "+req.Harness+" into ~/.local/bin and run its login flow; exit cleanly to save both.\r\n")
	case domain.WorkspaceShellHarnessLogin:
		_, _ = io.WriteString(conn, "aether: login shell ready. Run the agent's login flow; exit cleanly to save it.\r\n")
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
				if monitorErr := s.validateWorkspaceShellMember(ctx, member); monitorErr != nil {
					revoked <- monitorErr
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
	// exitError converts an observed container exit into the shell result:
	// transport errors and nonzero exits must both fail the session, or
	// sshd would report success (exit-status 0) for a failed bootstrap.
	exitError := func(status runtime.ExitStatus, waitErr error) error {
		if waitErr != nil {
			return waitErr
		}
		if status.Code != 0 {
			return fmt.Errorf("scheduler: workspace shell exited with status %d", status.Code)
		}
		return nil
	}
	select {
	case result := <-waitDone:
		cleanExit = result.err == nil && result.status.Code == 0
		terminalErr = exitError(result.status, result.err)
	case <-ctx.Done():
		terminalErr = ctx.Err()
	case revokedErr := <-revoked:
		terminalErr = fmt.Errorf("scheduler: workspace shell access revoked: %w", revokedErr)
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
			terminalErr = exitError(result.status, result.err)
		case <-waitCtx.Done():
			// Disconnect without an observed exit: an intentional detach.
			// Bootstrap keeps its pending staging for --resume; agent setup
			// must fail loudly, or sshd would report success for a session
			// that registered nothing.
			if req.Mode == domain.WorkspaceShellAgentSetup {
				terminalErr = errors.New("scheduler: agent setup detached before a clean exit; nothing was registered - rerun aether agent add")
			} else if needsStaging {
				_, _ = io.WriteString(conn, "\r\naether: session detached; pending tools staging preserved (resume with --resume)\r\n")
			}
		}
		cancel()
	}
	if terminalErr != nil || !cleanExit {
		return terminalErr
	}
	if needsStaging {
		manifest := domain.ToolManifest{}
		verify := req.VerificationExecutable
		if req.Mode == domain.WorkspaceShellAgentSetup {
			verify = stagedExecutable(req, profile)
		}
		// Vendor installers symlink ~/.local/bin entries to absolute paths
		// inside the container home; rewrite those relative so the tree
		// verifies on the host and survives a run user with the other home.
		if normalizeErr := normalizeStagedSymlinks(staging); normalizeErr != nil {
			return fmt.Errorf("scheduler: normalize staged tools: %w", normalizeErr)
		}
		if verify != "" {
			if verifyErr := verifyStagedExecutable(staging, verify); verifyErr != nil {
				slog.Warn("workspace shell verification failed", "mode", string(req.Mode), "executable", verify, "error", verifyErr)
				return fmt.Errorf("scheduler: verify bootstrap executable %q: %w", verify, verifyErr)
			}
			manifest.Executable = verify
		}
		slog.Info("workspace shell promoted", "mode", string(req.Mode))
		if _, promoteErr := s.cfg.Toolenv.Promote(ctx, string(member), string(ws.ID), staging, manifest, nil); promoteErr != nil {
			return promoteErr
		}
		if pendingID != "" {
			if deleteErr := s.cfg.Store.DeletePendingWorkspaceShell(ctx, pendingID); deleteErr != nil {
				return fmt.Errorf("scheduler: delete pending bootstrap session: %w", deleteErr)
			}
		}
	}
	if req.Mode == domain.WorkspaceShellAgentSetup {
		if registerErr := s.registerAgentDefinition(ctx, member, req, plan, conn); registerErr != nil {
			return registerErr
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
	if permissionErr := permissions.Check(permissions.Launch, permissions.Actor{ID: m.ID, Role: m.Role}, permissions.Target{}); permissionErr != nil {
		return fmt.Errorf("scheduler: workspace shell permission revoked: %w", permissionErr)
	}
	return nil
}

func purposeForShell(mode domain.WorkspaceShellMode) EnvironmentPurpose {
	switch mode {
	case domain.WorkspaceShellBootstrapTools:
		return EnvironmentPurposeBootstrap
	case domain.WorkspaceShellAgentSetup:
		return EnvironmentPurposeAgentSetup
	default:
		return EnvironmentPurposeLogin
	}
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
