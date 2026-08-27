package scheduler

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

const envTestDockerfile = "FROM ubuntu:24.04\nRUN apt-get update && apt-get install -y nodejs\n"

// saveEnvDefinition stores one valid single-item definition and returns it
// with its assigned version.
func saveEnvDefinition(t *testing.T, e *testEnv, itemVersion string) *domain.EnvironmentDefinition {
	t.Helper()
	def := &domain.EnvironmentDefinition{
		WorkspaceID: e.ws.ID,
		Dockerfile:  envTestDockerfile,
		Manifest: []domain.ManifestItem{{
			Name:         "node",
			Version:      itemVersion,
			StartLine:    2,
			EndLine:      2,
			CheckCommand: "node --version",
		}},
		Source: domain.EnvironmentSourceManual,
		Status: domain.EnvironmentSaved,
	}
	if err := e.db.SaveEnvironmentDefinition(t.Context(), def); err != nil {
		t.Fatalf("save environment definition: %v", err)
	}
	return def
}

// driveVerification makes every started container behave like the
// verification script: print one delimited section per manifest item with
// the given body, then exit 0. The definition version is read off the
// image tag so one hook serves builds of any version.
func driveVerification(e *testEnv, body string) {
	e.rt.startHook = func(c *fakeContainer) {
		version := 0
		if i := strings.LastIndex(c.spec.Image, ":"); i >= 0 {
			version, _ = strconv.Atoi(c.spec.Image[i+1:])
		}
		c.output(envVerifyMarker(version, 1, "BEGIN") + "\n" + body + "\n" + envVerifyMarker(version, 1, "END") + "\n")
		c.exitNow(0)
	}
}

func mustGetEnvDefinition(t *testing.T, e *testEnv, version int) *domain.EnvironmentDefinition {
	t.Helper()
	def, err := e.db.GetEnvironmentDefinition(t.Context(), e.ws.ID, version)
	if err != nil {
		t.Fatalf("get environment definition %d: %v", version, err)
	}
	return def
}

func workspaceImage(t *testing.T, e *testEnv) domain.WorkspaceEnvironment {
	t.Helper()
	ws, err := e.db.GetWorkspace(t.Context(), e.ws.ID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	return ws.Environment
}

func TestBuildEnvironmentActivatesAndSwapsImage(t *testing.T) {
	e := newTestEnv(t, nil)
	def := saveEnvDefinition(t, e, "20.0")
	driveVerification(e, "v20.0.1")
	sub := e.subscribe(t)

	if err := e.sched.BuildEnvironment(t.Context(), e.ws.ID, def.Version); err != nil {
		t.Fatalf("BuildEnvironment: %v", err)
	}

	stored := mustGetEnvDefinition(t, e, def.Version)
	if stored.Status != domain.EnvironmentActive {
		t.Errorf("status = %q, want %q", stored.Status, domain.EnvironmentActive)
	}
	if stored.FailureDetail != "" {
		t.Errorf("failure detail = %q, want empty", stored.FailureDetail)
	}
	env := workspaceImage(t, e)
	if env.CustomImage != def.ImageTag() {
		t.Errorf("workspace image = %q, want %q", env.CustomImage, def.ImageTag())
	}
	if env.NeutralImage {
		t.Error("neutral image still set after swap")
	}
	if !e.rt.hasImage(def.ImageTag()) {
		t.Errorf("image %s was not built", def.ImageTag())
	}

	seen := map[domain.EnvironmentStatus]bool{}
	sawLine := false
	deadline := time.After(waitTimeout)
	for !seen[domain.EnvironmentActive] {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatal("event stream closed before the active event")
			}
			p, isBuild := ev.Payload.(events.EnvironmentBuildPayload)
			if !isBuild {
				continue
			}
			if p.Version != def.Version {
				t.Errorf("event version = %d, want %d", p.Version, def.Version)
			}
			seen[p.Status] = true
			if p.Line != "" {
				sawLine = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for the active environment.build event")
		}
	}
	if !seen[domain.EnvironmentBuilding] || !seen[domain.EnvironmentVerifying] {
		t.Errorf("event statuses seen = %v, want building and verifying before active", seen)
	}
	if !sawLine {
		t.Error("no build output line reached the event feed")
	}
}

func TestBuildEnvironmentBuildFailurePreservesImage(t *testing.T) {
	e := newTestEnv(t, nil)
	def := saveEnvDefinition(t, e, "20.0")
	e.rt.buildErr = errors.New("daemon build failed: apt exited 100")

	err := e.sched.BuildEnvironment(t.Context(), e.ws.ID, def.Version)
	if err == nil || !strings.Contains(err.Error(), "apt exited 100") {
		t.Fatalf("BuildEnvironment error = %v, want the daemon detail", err)
	}
	stored := mustGetEnvDefinition(t, e, def.Version)
	if stored.Status != domain.EnvironmentFailed {
		t.Errorf("status = %q, want %q", stored.Status, domain.EnvironmentFailed)
	}
	if !strings.Contains(stored.FailureDetail, "apt exited 100") {
		t.Errorf("failure detail = %q, want the daemon detail", stored.FailureDetail)
	}
	env := workspaceImage(t, e)
	if env.CustomImage != "busybox:1.36" {
		t.Errorf("workspace image = %q, want the previous busybox:1.36", env.CustomImage)
	}
	if e.rt.hasImage(def.ImageTag()) {
		t.Errorf("failed build left image %s behind", def.ImageTag())
	}
}

func TestBuildEnvironmentVerificationMismatchNamesItem(t *testing.T) {
	e := newTestEnv(t, nil)
	def := saveEnvDefinition(t, e, "20.0")
	driveVerification(e, "v18.3.0")

	err := e.sched.BuildEnvironment(t.Context(), e.ws.ID, def.Version)
	if err == nil || !strings.Contains(err.Error(), `"node"`) {
		t.Fatalf("BuildEnvironment error = %v, want the mismatching item named", err)
	}
	stored := mustGetEnvDefinition(t, e, def.Version)
	if stored.Status != domain.EnvironmentFailed {
		t.Errorf("status = %q, want %q", stored.Status, domain.EnvironmentFailed)
	}
	if !strings.Contains(stored.FailureDetail, `"node"`) || !strings.Contains(stored.FailureDetail, "20.0") {
		t.Errorf("failure detail = %q, want the item and expected version named", stored.FailureDetail)
	}
	env := workspaceImage(t, e)
	if env.CustomImage != "busybox:1.36" {
		t.Errorf("workspace image = %q, want the previous busybox:1.36", env.CustomImage)
	}
}

func TestBuildEnvironmentSerializesPerWorkspace(t *testing.T) {
	e := newTestEnv(t, nil)
	v1 := saveEnvDefinition(t, e, "20.0")
	v2 := saveEnvDefinition(t, e, "20.0")
	driveVerification(e, "v20.0.1")

	entered := make(chan string, 2)
	release := make(chan struct{})
	e.rt.buildHook = func(tag string) {
		entered <- tag
		<-release
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = e.sched.BuildEnvironment(t.Context(), e.ws.ID, v1.Version) }()
	go func() { defer wg.Done(); errs[1] = e.sched.BuildEnvironment(t.Context(), e.ws.ID, v2.Version) }()

	first := <-entered
	select {
	case second := <-entered:
		t.Fatalf("build of %s entered the engine while %s was still building", second, first)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("second build never reached the engine after the first finished")
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("build %d: %v", i+1, err)
		}
	}
}

func TestRetentionPrunesBeyondActiveAndPrevious(t *testing.T) {
	e := newTestEnv(t, nil)
	driveVerification(e, "v20.0.1")
	var defs []*domain.EnvironmentDefinition
	for i := 0; i < 3; i++ {
		d := saveEnvDefinition(t, e, "20.0")
		if err := e.sched.BuildEnvironment(t.Context(), e.ws.ID, d.Version); err != nil {
			t.Fatalf("BuildEnvironment %d: %v", d.Version, err)
		}
		defs = append(defs, d)
	}
	if e.rt.hasImage(defs[0].ImageTag()) {
		t.Errorf("retention kept prunable tag %s", defs[0].ImageTag())
	}
	if !e.rt.hasImage(defs[1].ImageTag()) || !e.rt.hasImage(defs[2].ImageTag()) {
		t.Error("retention removed the active or previously active tag")
	}
	env := workspaceImage(t, e)
	if env.CustomImage != defs[2].ImageTag() {
		t.Errorf("workspace image = %q, want %q", env.CustomImage, defs[2].ImageTag())
	}
}

func TestRollbackReactivatesPreviousVersion(t *testing.T) {
	e := newTestEnv(t, nil)
	driveVerification(e, "v20.0.1")
	v1 := saveEnvDefinition(t, e, "20.0")
	if err := e.sched.BuildEnvironment(t.Context(), e.ws.ID, v1.Version); err != nil {
		t.Fatalf("BuildEnvironment 1: %v", err)
	}
	v2 := saveEnvDefinition(t, e, "20.0")
	if err := e.sched.BuildEnvironment(t.Context(), e.ws.ID, v2.Version); err != nil {
		t.Fatalf("BuildEnvironment 2: %v", err)
	}

	buildsBefore := e.rt.buildCount()
	version, err := e.sched.RollbackEnvironment(t.Context(), e.ws.ID)
	if err != nil {
		t.Fatalf("RollbackEnvironment: %v", err)
	}
	if version != v1.Version {
		t.Errorf("rolled back to version %d, want %d", version, v1.Version)
	}
	if e.rt.buildCount() != buildsBefore {
		t.Error("rollback rebuilt an image whose tag was still present")
	}
	if got := mustGetEnvDefinition(t, e, v1.Version).Status; got != domain.EnvironmentActive {
		t.Errorf("version 1 status = %q, want %q", got, domain.EnvironmentActive)
	}
	if got := mustGetEnvDefinition(t, e, v2.Version).Status; got != domain.EnvironmentSaved {
		t.Errorf("version 2 status = %q, want %q", got, domain.EnvironmentSaved)
	}
	env := workspaceImage(t, e)
	if env.CustomImage != v1.ImageTag() {
		t.Errorf("workspace image = %q, want %q", env.CustomImage, v1.ImageTag())
	}
}

func TestRollbackRebuildsWhenTagIsGone(t *testing.T) {
	e := newTestEnv(t, nil)
	driveVerification(e, "v20.0.1")
	v1 := saveEnvDefinition(t, e, "20.0")
	if err := e.sched.BuildEnvironment(t.Context(), e.ws.ID, v1.Version); err != nil {
		t.Fatalf("BuildEnvironment 1: %v", err)
	}
	v2 := saveEnvDefinition(t, e, "20.0")
	if err := e.sched.BuildEnvironment(t.Context(), e.ws.ID, v2.Version); err != nil {
		t.Fatalf("BuildEnvironment 2: %v", err)
	}
	if err := e.rt.RemoveImage(t.Context(), v1.ImageTag()); err != nil {
		t.Fatalf("remove image: %v", err)
	}

	version, err := e.sched.RollbackEnvironment(t.Context(), e.ws.ID)
	if err != nil {
		t.Fatalf("RollbackEnvironment: %v", err)
	}
	if version != v1.Version {
		t.Errorf("rolled back to version %d, want %d", version, v1.Version)
	}
	if !e.rt.hasImage(v1.ImageTag()) {
		t.Errorf("rollback did not rebuild the purged tag %s", v1.ImageTag())
	}
	if got := mustGetEnvDefinition(t, e, v1.Version).Status; got != domain.EnvironmentActive {
		t.Errorf("version 1 status = %q, want %q", got, domain.EnvironmentActive)
	}
	env := workspaceImage(t, e)
	if env.CustomImage != v1.ImageTag() {
		t.Errorf("workspace image = %q, want %q", env.CustomImage, v1.ImageTag())
	}
}
