package scheduler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/envdef"
	"github.com/3xDevOps/Aether/internal/envprompt"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// environmentEditOutputDir is the container path of the writable scratch
// mount the edit agent writes its revised Dockerfile and manifest into.
const environmentEditOutputDir = "/aether/env-edit"

// environmentEditTimeout bounds one edit-agent invocation, matching the
// local inventory engine's default.
const environmentEditTimeout = 10 * time.Minute

// ErrEnvironmentEditPreflight marks an environment edit refused before
// anything ran: the chosen agent cannot drive environment edits, or it
// has no login state on this server. The message always names the
// aether agent add command that fixes it, so surfaces can show it
// verbatim.
var ErrEnvironmentEditPreflight = errors.New("scheduler: the edit agent is not ready")

// EditEnvironment runs the workspace's chosen harness headless in a
// throwaway container against the current environment definition and the
// admin's plain-language request, validates the revised pair it writes
// (retrying once with the validation error appended), and stores the
// result as a new saved definition version - a proposal nothing builds
// until env.build approves it. Progress, agent output, and the terminal
// state ride environment.edit events; the returned version is the
// proposal. Edits serialize with builds per workspace.
func (s *Scheduler) EditEnvironment(ctx context.Context, workspaceID domain.WorkspaceID, member domain.MemberID, harnessName, request string) (int, error) {
	version, err := s.editEnvironment(ctx, workspaceID, member, harnessName, request)
	if err != nil {
		// The failure event is the async caller's only channel; write it
		// detached from ctx so a canceled edit still reports.
		s.publishEnvironmentEdit(context.WithoutCancel(ctx), workspaceID, harnessName, events.EnvironmentEditFailed, "", err.Error(), 0)
		return 0, err
	}
	return version, nil
}

func (s *Scheduler) editEnvironment(ctx context.Context, workspaceID domain.WorkspaceID, member domain.MemberID, harnessName, request string) (int, error) {
	if strings.TrimSpace(request) == "" {
		return 0, errors.New("scheduler: environment edit needs a change request")
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("scheduler: load workspace for environment edit: %w", err)
	}
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return 0, fmt.Errorf("scheduler: load member for environment edit: %w", err)
	}
	if err = s.checkEnvironmentEditAgent(ctx, member, ws, harnessName); err != nil {
		return 0, err
	}
	// The build lock serializes edits against builds and rollbacks: the
	// definition an edit revises must not change underneath it.
	unlock := s.lockEnvironmentBuild(workspaceID)
	defer unlock()
	base, err := s.environmentEditBase(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	var pair *environmentEditPair
	if harnessName == "fake" {
		pair, err = s.fakeEnvironmentEdit(ctx, workspaceID)
	} else {
		pair, err = s.runEnvironmentEdit(ctx, ws, m, harnessName, base, request)
	}
	if err != nil {
		return 0, err
	}
	def := &domain.EnvironmentDefinition{
		WorkspaceID: workspaceID,
		Dockerfile:  pair.dockerfile,
		Manifest:    pair.manifest,
		// The proposal keeps the provenance of the version it edited; the
		// editing harness is recorded as the author of this version.
		Source:  base.Source,
		Harness: harnessName,
		Status:  domain.EnvironmentSaved,
	}
	if err := s.cfg.Store.SaveEnvironmentDefinition(ctx, def); err != nil {
		return 0, fmt.Errorf("scheduler: save proposed environment version: %w", err)
	}
	s.publishEnvironmentEdit(ctx, workspaceID, harnessName, events.EnvironmentEditProposed, "", "", def.Version)
	return def.Version, nil
}

// checkEnvironmentEditAgent refuses an edit that could only fail inside a
// container: the harness must be setup-capable and the member must have
// login state for it on this server (a non-empty credential home or a
// registered member definition). checkAgentInstalled cannot cover this -
// it skips custom-image workspaces, and an edit workspace usually runs a
// custom image. The fake harness never launches a vendor CLI, so it
// skips the check.
func (s *Scheduler) checkEnvironmentEditAgent(ctx context.Context, member domain.MemberID, ws *domain.Workspace, harnessName string) error {
	if harnessName == "fake" {
		return nil
	}
	setupCapable := false
	for _, p := range harness.SetupHarnesses() {
		if p.Name == harnessName {
			setupCapable = true
			break
		}
	}
	if !setupCapable {
		return fmt.Errorf("%w: %q cannot edit environments; pick claude, codex, pi, or amp and register it with: aether agent add <agent> --workspace %s",
			ErrEnvironmentEditPreflight, harnessName, ws.Name)
	}
	if s.cfg.HomesDir != "" {
		home := filepath.Join(s.cfg.HomesDir, string(member), harnessName)
		if entries, err := os.ReadDir(home); err == nil && len(entries) > 0 {
			return nil
		}
	}
	_, err := s.cfg.Store.GetHarnessDefinition(ctx, member, harnessName)
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("scheduler: load member harness definition: %w", err)
	}
	return fmt.Errorf("%w: %s has no login on this server; register it with: aether agent add %s --workspace %s",
		ErrEnvironmentEditPreflight, harnessName, harnessName, ws.Name)
}

// environmentEditBase picks the definition an edit revises: the active
// version, or the newest one before anything has activated.
func (s *Scheduler) environmentEditBase(ctx context.Context, workspaceID domain.WorkspaceID) (*domain.EnvironmentDefinition, error) {
	active, err := s.cfg.Store.GetActiveEnvironmentDefinition(ctx, workspaceID)
	if err == nil {
		return active, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("scheduler: load active environment definition: %w", err)
	}
	defs, err := s.cfg.Store.ListEnvironmentDefinitions(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: list environment definitions: %w", err)
	}
	if len(defs) == 0 {
		return nil, errors.New("scheduler: the workspace has no environment definition to edit; save one first")
	}
	return defs[0], nil // newest first
}

// environmentEditPair is a validated edit result.
type environmentEditPair struct {
	dockerfile string
	manifest   []domain.ManifestItem
}

// runEnvironmentEdit is the container twin of the local gateway's
// RunScan refine mode: render the refine prompt around the base pair and
// the request, run the harness headless with the run plan's mounts plus
// a writable scratch mount, validate what it wrote, and retry once with
// the validation error appended to the prompt.
func (s *Scheduler) runEnvironmentEdit(ctx context.Context, ws *domain.Workspace, member *domain.Member, harnessName string, base *domain.EnvironmentDefinition, request string) (*environmentEditPair, error) {
	if s.cfg.EnvEditDir == "" {
		return nil, errors.New("scheduler: the environment edit scratch root is not configured")
	}
	manifestJSON, err := json.MarshalIndent(base.Manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("scheduler: encode the manifest for the edit prompt: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			s.publishEnvironmentEdit(ctx, ws.ID, harnessName, events.EnvironmentEditRetrying, "", "", 0)
		}
		prompt, promptErr := envprompt.RenderRefine(envprompt.RefineParams{
			Dockerfile:   base.Dockerfile,
			ManifestJSON: string(manifestJSON),
			Feedback:     request,
			OutputDir:    environmentEditOutputDir,
		})
		if promptErr != nil {
			return nil, fmt.Errorf("scheduler: render the edit prompt: %w", promptErr)
		}
		if lastErr != nil {
			prompt += "\n\nYour previous attempt failed validation:\n" + lastErr.Error() +
				"\n\nCorrect these problems and write both files again."
		}
		pair, attemptErr, retryable := s.environmentEditAttempt(ctx, ws, member, harnessName, prompt)
		if attemptErr == nil {
			return pair, nil
		}
		if !retryable {
			return nil, attemptErr
		}
		lastErr = attemptErr
	}
	return nil, fmt.Errorf("scheduler: the edit agent's output failed validation twice: %w", lastErr)
}

// environmentEditAttempt runs the harness once in a throwaway container
// and validates the pair it wrote into the scratch directory. retryable
// marks contract violations (missing or invalid output files) that earn
// the one retry; execution failures do not.
func (s *Scheduler) environmentEditAttempt(ctx context.Context, ws *domain.Workspace, member *domain.Member, harnessName, prompt string) (pair *environmentEditPair, err error, retryable bool) {
	argv, profile, err := s.command(ctx, member.ID, harnessName, domain.LaunchHeadless, prompt)
	if err != nil {
		return nil, err, false
	}
	// The run-purpose plan brings exactly what a run gets and nothing
	// more: the member's credential mounts and read-only tool snapshot on
	// the workspace's effective image. No aether secrets.
	plan, err := s.BuildEnvironmentPlan(ctx, nil, ws, member, profile, EnvironmentPurposeRun, "")
	if err != nil {
		return nil, err, false
	}
	reservation, err := s.reserveCredentialUser(member.ID, harnessName, plan.User, len(plan.Mounts) > 0, "environment edit", nil)
	if err != nil {
		return nil, err, false
	}
	defer s.releaseCredentialUser(reservation)
	if err = os.MkdirAll(s.cfg.EnvEditDir, 0o700); err != nil {
		return nil, fmt.Errorf("scheduler: create the edit scratch root: %w", err), false
	}
	scratch, err := os.MkdirTemp(s.cfg.EnvEditDir, "edit-")
	if err != nil {
		return nil, fmt.Errorf("scheduler: create the edit scratch directory: %w", err), false
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	// The scratch mount is appended after the plan's own validated
	// mounts, the way coordination mounts are, and validated on its own
	// against the server-owned scratch root.
	scratchMount := []runtime.Mount{{HostPath: scratch, ContainerPath: environmentEditOutputDir}}
	if err = runtime.ValidateMounts(scratchMount, runtime.MountPolicy{OwnedRoots: []string{s.cfg.EnvEditDir}}); err != nil {
		return nil, fmt.Errorf("scheduler: validate the edit scratch mount: %w", err), false
	}
	plan.Mounts = append(plan.Mounts, scratchMount...)
	if err = s.applyRunOwnership(ws, &domain.Run{}, plan.Mounts, plan.User); err != nil {
		return nil, fmt.Errorf("scheduler: environment edit ownership: %w", err), false
	}
	var nonce [4]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("scheduler: edit container nonce: %w", err), false
	}
	name := fmt.Sprintf("env-edit-%s-%s", ws.ID, hex.EncodeToString(nonce[:]))
	runCtx, cancel := context.WithTimeout(ctx, environmentEditTimeout)
	defer cancel()
	id, err := s.cfg.Runtime.Create(runCtx, runtime.Spec{
		Name:        name,
		Image:       plan.Image,
		Env:         plan.Env,
		SetupScript: plan.SetupScript,
		WorkingDir:  plan.Home,
		Command:     argv,
		Mounts:      plan.Mounts,
		User:        plan.User,
		CreationKey: name,
	})
	if err != nil {
		return nil, fmt.Errorf("scheduler: create the edit container: %w", err), false
	}
	defer func() { _ = s.cfg.Runtime.Destroy(context.WithoutCancel(ctx), id) }()
	att, err := s.cfg.Runtime.Attach(runCtx, id)
	if err != nil {
		return nil, fmt.Errorf("scheduler: attach the edit container: %w", err), false
	}
	defer func() { _ = att.Close() }()
	log := &environmentEditLog{s: s, ctx: ctx, workspace: ws.ID, harness: harnessName}
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() { defer pumps.Done(); _, _ = io.Copy(log, att.Stdout()) }()
	go func() { defer pumps.Done(); _, _ = io.Copy(log, att.Stderr()) }()
	s.publishEnvironmentEdit(ctx, ws.ID, harnessName, events.EnvironmentEditRunning, "", "", 0)
	if err = s.cfg.Runtime.Start(runCtx, id); err != nil {
		return nil, fmt.Errorf("scheduler: start the edit container: %w", err), false
	}
	status, err := s.cfg.Runtime.Wait(runCtx, id)
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("scheduler: the edit agent timed out after %s", environmentEditTimeout), false
		}
		return nil, fmt.Errorf("scheduler: wait for the edit container: %w", err), false
	}
	pumps.Wait()
	log.flush()
	if status.Code != 0 {
		return nil, fmt.Errorf("scheduler: the edit agent exited with status %d", status.Code), false
	}
	s.publishEnvironmentEdit(ctx, ws.ID, harnessName, events.EnvironmentEditValidating, "", "", 0)
	pair, err = collectEnvironmentEditOutput(scratch)
	if err != nil {
		return nil, err, true
	}
	return pair, nil, false
}

// collectEnvironmentEditOutput reads and validates the two files the
// output contract requires the agent to write into the scratch mount.
// The messages skip the package prefix because they are fed back to the
// agent verbatim on the retry.
func collectEnvironmentEditOutput(scratch string) (*environmentEditPair, error) {
	dockerfile, err := os.ReadFile(filepath.Join(scratch, "Dockerfile"))
	if err != nil {
		return nil, fmt.Errorf("the agent did not write Dockerfile into %s: %w", environmentEditOutputDir, err)
	}
	manifestJSON, err := os.ReadFile(filepath.Join(scratch, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("the agent did not write manifest.json into %s: %w", environmentEditOutputDir, err)
	}
	items, err := envdef.ParseManifest(manifestJSON)
	if err != nil {
		return nil, err
	}
	if err := envdef.ValidateDockerfile(string(dockerfile), items); err != nil {
		return nil, err
	}
	return &environmentEditPair{dockerfile: string(dockerfile), manifest: items}, nil
}

// Canned edited pair for the "fake" harness, mirroring the local scan
// fakes: it must always satisfy the envdef contract and differ from the
// canned inventory so a demo edit shows a real diff.
const fakeEditDockerfile = `FROM ubuntu:24.04

RUN apt-get update \
    && apt-get install -y --no-install-recommends jq=1.7.1-3build1 ripgrep=14.1.0-1 \
    && rm -rf /var/lib/apt/lists/*
`

const fakeEditManifestJSON = `[
  {
    "name": "jq",
    "version": "1.7.1",
    "reason": "canned edit item for demos and tests",
    "start_line": 3,
    "end_line": 5,
    "check_command": "jq --version"
  },
  {
    "name": "ripgrep",
    "version": "14.1.0",
    "reason": "canned edit item for demos and tests",
    "start_line": 3,
    "end_line": 5,
    "check_command": "rg --version"
  }
]
`

// fakeEnvironmentEdit short-circuits to the canned pair through the same
// validation a real edit uses, so the whole flow is demoable and
// testable without vendor CLIs and the fake can never drift from the
// contract.
func (s *Scheduler) fakeEnvironmentEdit(ctx context.Context, workspaceID domain.WorkspaceID) (*environmentEditPair, error) {
	s.publishEnvironmentEdit(ctx, workspaceID, "fake", events.EnvironmentEditRunning, "", "", 0)
	s.publishEnvironmentEdit(ctx, workspaceID, "fake", events.EnvironmentEditRunning, "fake harness: returning the canned edited pair", "", 0)
	s.publishEnvironmentEdit(ctx, workspaceID, "fake", events.EnvironmentEditValidating, "", "", 0)
	items, err := envdef.ParseManifest([]byte(fakeEditManifestJSON))
	if err != nil {
		return nil, fmt.Errorf("scheduler: canned fake edit manifest: %w", err)
	}
	if err := envdef.ValidateDockerfile(fakeEditDockerfile, items); err != nil {
		return nil, fmt.Errorf("scheduler: canned fake edit Dockerfile: %w", err)
	}
	return &environmentEditPair{dockerfile: fakeEditDockerfile, manifest: items}, nil
}

func (s *Scheduler) publishEnvironmentEdit(ctx context.Context, workspaceID domain.WorkspaceID, harnessName string, status events.EnvironmentEditStatus, line, detail string, version int) {
	s.publish(ctx, events.Event{
		WorkspaceID: workspaceID,
		Payload: events.EnvironmentEditPayload{
			Harness: harnessName,
			Status:  status,
			Line:    line,
			Detail:  detail,
			Version: version,
		},
	})
}

// environmentEditLog turns the edit container's output into one
// environment.edit event per line. Stdout and stderr share one instance
// from separate goroutines, so Write locks.
type environmentEditLog struct {
	s         *Scheduler
	ctx       context.Context
	workspace domain.WorkspaceID
	harness   string

	mu  sync.Mutex
	buf []byte
}

func (w *environmentEditLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.emit(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
}

// flush emits any trailing output the agent did not newline-terminate.
func (w *environmentEditLog) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

func (w *environmentEditLog) emit(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	w.s.publishEnvironmentEdit(w.ctx, w.workspace, w.harness, events.EnvironmentEditRunning, line, "", 0)
}
