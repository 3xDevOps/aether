package scheduler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/envdef"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// imageExistsProber is the optional runtime capability the rollback path
// uses to decide whether a target version's tag must be rebuilt
// (*runtime.Docker implements it). Runtimes without it always rebuild:
// locally built tags are never pulled, so rebuilding from the stored
// Dockerfile is the only safe assumption.
type imageExistsProber interface {
	ImageExists(ctx context.Context, ref string) (bool, error)
}

// BuildEnvironment builds one stored environment definition version into
// the workspace's image: validate, build, verify against the manifest's
// check commands in a throwaway container, then atomically activate the
// version, swap the workspace image to its tag, and prune old tags.
// Failures mark the version failed and leave the workspace image
// untouched. Builds serialize per workspace.
func (s *Scheduler) BuildEnvironment(ctx context.Context, workspaceID domain.WorkspaceID, version int) error {
	unlock := s.lockEnvironmentBuild(workspaceID)
	defer unlock()
	return s.buildEnvironmentLocked(ctx, workspaceID, version)
}

// RollbackEnvironment re-activates the most recent non-failed version
// below the active one and returns its version number. If retention
// already removed that version's tag, it is rebuilt (and re-verified)
// from the stored Dockerfile first, so rollback never depends on a
// registry.
func (s *Scheduler) RollbackEnvironment(ctx context.Context, workspaceID domain.WorkspaceID) (int, error) {
	unlock := s.lockEnvironmentBuild(workspaceID)
	defer unlock()
	active, err := s.cfg.Store.GetActiveEnvironmentDefinition(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, fmt.Errorf("scheduler: workspace %s has no active environment version to roll back from", workspaceID)
		}
		return 0, fmt.Errorf("scheduler: load active environment definition: %w", err)
	}
	defs, err := s.cfg.Store.ListEnvironmentDefinitions(ctx, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("scheduler: list environment definitions: %w", err)
	}
	var target *domain.EnvironmentDefinition
	for _, d := range defs { // newest first
		if d.Version < active.Version && d.Status == domain.EnvironmentSaved {
			target = d
			break
		}
	}
	if target == nil {
		return 0, fmt.Errorf("scheduler: workspace %s has no previous environment version to roll back to", workspaceID)
	}
	exists := false
	if prober, ok := s.cfg.Runtime.(imageExistsProber); ok {
		exists, err = prober.ImageExists(ctx, target.ImageTag())
		if err != nil {
			return 0, fmt.Errorf("scheduler: check rollback image %s: %w", target.ImageTag(), err)
		}
	}
	if !exists {
		if err := s.buildEnvironmentLocked(ctx, workspaceID, target.Version); err != nil {
			return 0, fmt.Errorf("scheduler: rebuild environment version %d for rollback: %w", target.Version, err)
		}
		return target.Version, nil
	}
	if err := s.activateEnvironment(ctx, target); err != nil {
		return 0, err
	}
	return target.Version, nil
}

// lockEnvironmentBuild claims the workspace's build slot, blocking until
// any running build releases it.
func (s *Scheduler) lockEnvironmentBuild(workspaceID domain.WorkspaceID) func() {
	s.mu.Lock()
	if s.envBuildLocks == nil {
		s.envBuildLocks = make(map[domain.WorkspaceID]*sync.Mutex)
	}
	lock := s.envBuildLocks[workspaceID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.envBuildLocks[workspaceID] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (s *Scheduler) buildEnvironmentLocked(ctx context.Context, workspaceID domain.WorkspaceID, version int) error {
	def, err := s.cfg.Store.GetEnvironmentDefinition(ctx, workspaceID, version)
	if err != nil {
		return fmt.Errorf("scheduler: load environment definition %d for workspace %s: %w", version, workspaceID, err)
	}
	// The definition was validated at save time; revalidate against the
	// current contract anyway so a stale stored row can never reach the
	// daemon.
	if err := envdef.ValidateDockerfile(def.Dockerfile, def.Manifest); err != nil {
		return s.failEnvironmentBuild(ctx, workspaceID, version, fmt.Errorf("scheduler: stored environment definition is invalid: %w", err))
	}
	if err := s.setEnvironmentStatus(ctx, workspaceID, version, domain.EnvironmentBuilding, ""); err != nil {
		return err
	}
	progress := &environmentBuildLog{s: s, ctx: ctx, workspace: workspaceID, version: version}
	if err := s.cfg.Runtime.BuildImage(ctx, def.Dockerfile, def.ImageTag(), progress); err != nil {
		return s.failEnvironmentBuild(ctx, workspaceID, version, fmt.Errorf("scheduler: build environment image %s: %w", def.ImageTag(), err))
	}
	progress.flush()
	if err := s.setEnvironmentStatus(ctx, workspaceID, version, domain.EnvironmentVerifying, ""); err != nil {
		return err
	}
	if err := s.verifyEnvironmentImage(ctx, def); err != nil {
		return s.failEnvironmentBuild(ctx, workspaceID, version, err)
	}
	return s.activateEnvironment(ctx, def)
}

// failEnvironmentBuild records cause as the version's failure and returns
// it. The bookkeeping writes run detached from ctx so a canceled build
// still lands in the failed state instead of wedging in building.
func (s *Scheduler) failEnvironmentBuild(ctx context.Context, workspaceID domain.WorkspaceID, version int, cause error) error {
	bg := context.WithoutCancel(ctx)
	if err := s.cfg.Store.SetEnvironmentStatus(bg, workspaceID, version, domain.EnvironmentFailed, cause.Error()); err != nil {
		return errors.Join(cause, fmt.Errorf("scheduler: mark environment version %d failed: %w", version, err))
	}
	s.publishEnvironmentBuild(bg, workspaceID, version, domain.EnvironmentFailed, "", cause.Error())
	return cause
}

func (s *Scheduler) setEnvironmentStatus(ctx context.Context, workspaceID domain.WorkspaceID, version int, status domain.EnvironmentStatus, detail string) error {
	if err := s.cfg.Store.SetEnvironmentStatus(ctx, workspaceID, version, status, detail); err != nil {
		return fmt.Errorf("scheduler: mark environment version %d %s: %w", version, status, err)
	}
	s.publishEnvironmentBuild(ctx, workspaceID, version, status, "", detail)
	return nil
}

// activateEnvironment makes def the workspace's environment: activate the
// version (the store demotes the previous active one atomically), point
// the workspace image at the tag, and prune tags beyond active plus the
// version that was active until now.
func (s *Scheduler) activateEnvironment(ctx context.Context, def *domain.EnvironmentDefinition) error {
	previousVersion := 0
	previous, err := s.cfg.Store.GetActiveEnvironmentDefinition(ctx, def.WorkspaceID)
	switch {
	case err == nil:
		previousVersion = previous.Version
	case !errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("scheduler: load previously active environment: %w", err)
	}
	if err = s.cfg.Store.SetEnvironmentStatus(ctx, def.WorkspaceID, def.Version, domain.EnvironmentActive, ""); err != nil {
		return fmt.Errorf("scheduler: activate environment version %d: %w", def.Version, err)
	}
	workspace, err := s.cfg.Store.GetWorkspace(ctx, def.WorkspaceID)
	if err != nil {
		return fmt.Errorf("scheduler: load workspace for image swap: %w", err)
	}
	workspace.Environment.CustomImage = def.ImageTag()
	workspace.Environment.NeutralImage = false
	if err := s.cfg.Store.UpdateWorkspace(ctx, workspace); err != nil {
		return fmt.Errorf("scheduler: swap workspace image to %s: %w", def.ImageTag(), err)
	}
	if err := s.pruneEnvironmentImages(ctx, def.WorkspaceID, def.Version, previousVersion); err != nil {
		return err
	}
	s.publishEnvironmentBuild(ctx, def.WorkspaceID, def.Version, domain.EnvironmentActive, "", "")
	return nil
}

// pruneEnvironmentImages removes every version tag except the two kept for
// rollback: the newly active version and the one active before it.
func (s *Scheduler) pruneEnvironmentImages(ctx context.Context, workspaceID domain.WorkspaceID, activeVersion, previousVersion int) error {
	defs, err := s.cfg.Store.ListEnvironmentDefinitions(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("scheduler: list environment definitions for retention: %w", err)
	}
	var errs []error
	for _, d := range defs {
		if d.Version == activeVersion || d.Version == previousVersion {
			continue
		}
		if err := s.cfg.Runtime.RemoveImage(ctx, d.ImageTag()); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("scheduler: prune environment images: %w", errors.Join(errs...))
	}
	return nil
}

// verifyEnvironmentImage boots one throwaway container from the freshly
// built tag and runs the marker-delimited check script; each manifest
// item's declared version must appear in its check command's output.
func (s *Scheduler) verifyEnvironmentImage(ctx context.Context, def *domain.EnvironmentDefinition) error {
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("scheduler: verification nonce: %w", err)
	}
	name := fmt.Sprintf("env-verify-%s-%d-%s", def.WorkspaceID, def.Version, hex.EncodeToString(nonce[:]))
	id, err := s.cfg.Runtime.Create(ctx, runtime.Spec{
		Name:        name,
		Image:       def.ImageTag(),
		Command:     []string{"/bin/sh", "-c", environmentCheckScript(def)},
		CreationKey: name,
	})
	if err != nil {
		return fmt.Errorf("scheduler: create verification container: %w", err)
	}
	defer func() { _ = s.cfg.Runtime.Destroy(context.WithoutCancel(ctx), id) }()
	att, err := s.cfg.Runtime.Attach(ctx, id)
	if err != nil {
		return fmt.Errorf("scheduler: attach verification container: %w", err)
	}
	defer func() { _ = att.Close() }()
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(&out, att.Stdout())
	}()
	go func() { _, _ = io.Copy(io.Discard, att.Stderr()) }()
	if err = s.cfg.Runtime.Start(ctx, id); err != nil {
		return fmt.Errorf("scheduler: start verification container: %w", err)
	}
	status, err := s.cfg.Runtime.Wait(ctx, id)
	if err != nil {
		return fmt.Errorf("scheduler: wait for verification container: %w", err)
	}
	<-done
	if status.Code != 0 {
		return fmt.Errorf("scheduler: environment verification script exited %d", status.Code)
	}
	return verifyEnvironmentOutput(out.String(), def)
}

// envVerifyMarker is the line printed before (BEGIN) and after (END) each
// check command's output so the sections can be cut apart reliably.
func envVerifyMarker(version, item int, edge string) string {
	return fmt.Sprintf("AETHER-VERIFY-%d-%d-%s", version, item, edge)
}

// environmentCheckScript generates the verification shell script: every
// manifest item's check command runs between its unique markers with
// stderr folded into stdout, and a failing command never stops the script
// (its section simply lacks the version).
func environmentCheckScript(def *domain.EnvironmentDefinition) string {
	var b strings.Builder
	b.WriteString("set -u\n")
	for i, item := range def.Manifest {
		fmt.Fprintf(&b, "echo %s\n", envVerifyMarker(def.Version, i+1, "BEGIN"))
		fmt.Fprintf(&b, "{\n%s\n} 2>&1\n", item.CheckCommand)
		fmt.Fprintf(&b, "echo %s\n", envVerifyMarker(def.Version, i+1, "END"))
	}
	return b.String()
}

// verifyEnvironmentOutput checks every manifest item's section of the
// captured script output, reporting all mismatches together with the item
// named in each.
func verifyEnvironmentOutput(output string, def *domain.EnvironmentDefinition) error {
	var errs []error
	for i, item := range def.Manifest {
		section, ok := cutEnvironmentSection(output, def.Version, i+1)
		if !ok {
			errs = append(errs, fmt.Errorf("item %q: check command produced no delimited output", item.Name))
			continue
		}
		if !strings.Contains(section, item.Version) {
			errs = append(errs, fmt.Errorf("item %q: version %q not found in check output: %s",
				item.Name, item.Version, truncateCheckOutput(section)))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("scheduler: environment verification failed: %w", errors.Join(errs...))
	}
	return nil
}

func cutEnvironmentSection(output string, version, item int) (string, bool) {
	_, after, ok := strings.Cut(output, envVerifyMarker(version, item, "BEGIN"))
	if !ok {
		return "", false
	}
	section, _, ok := strings.Cut(after, envVerifyMarker(version, item, "END"))
	if !ok {
		return "", false
	}
	return section, true
}

// maxCheckOutputDetail bounds one check command's output inside a failure
// detail; the full output never needs to ride a status row.
const maxCheckOutputDetail = 256

func truncateCheckOutput(section string) string {
	section = strings.TrimSpace(section)
	if runes := []rune(section); len(runes) > maxCheckOutputDetail {
		section = string(runes[:maxCheckOutputDetail]) + "..."
	}
	return section
}

func (s *Scheduler) publishEnvironmentBuild(ctx context.Context, workspaceID domain.WorkspaceID, version int, status domain.EnvironmentStatus, line, detail string) {
	s.publish(ctx, events.Event{
		WorkspaceID: workspaceID,
		Payload:     events.EnvironmentBuildPayload{Version: version, Status: status, Line: line, Detail: detail},
	})
}

// environmentBuildLog turns the engine's build progress stream into one
// environment.build event per output line.
type environmentBuildLog struct {
	s         *Scheduler
	ctx       context.Context
	workspace domain.WorkspaceID
	version   int
	buf       []byte
}

func (w *environmentBuildLog) Write(p []byte) (int, error) {
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

// flush emits any trailing output the engine did not newline-terminate.
func (w *environmentBuildLog) flush() {
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

func (w *environmentBuildLog) emit(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	w.s.publishEnvironmentBuild(w.ctx, w.workspace, w.version, domain.EnvironmentBuilding, line, "")
}
