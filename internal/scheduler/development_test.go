package scheduler

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func developmentFixture(t *testing.T) (*testEnv, *domain.Run) {
	t.Helper()
	e := newTestEnv(t, func(c *Config) { c.Control = control.New(control.Config{}) })
	run, _ := e.launchFake(t, "development boundary")
	e.sched.mu.Lock()
	e.sched.runs[run.ID].coordDir = filepath.Join(t.TempDir(), "coord")
	e.sched.mu.Unlock()
	return e, run
}
func captureFixture(t *testing.T, s *Scheduler, id domain.RunID) protocol.DevArtifact {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{B: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	capture := browser.Capture{Bytes: encoded.Bytes(), Metadata: browser.Metadata{Page: browser.Page{SessionID: "session-1", PageID: "page-1", PageRevision: 1, ViewportID: "viewport-1", Width: 2, Height: 1}, ContentType: "image/png", CapturedAt: time.Now().UTC()}}
	artifact, err := s.saveDevelopmentCapture(t.Context(), id, "browser", "", capture, 0)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}
func TestDevelopmentSocketIdentityCannotSelectAnotherRun(t *testing.T) {
	e, run := developmentFixture(t)
	for _, raw := range []json.RawMessage{json.RawMessage(`{"run_id":"other"}`), json.RawMessage(`{"run_id":""}`), json.RawMessage(`{"run_id":null}`)} {
		if _, err := e.sched.HandleAgent(t.Context(), run.ID, protocol.MethodDevArtifactList, raw); err == nil {
			t.Fatalf("accepted caller run selector %s", raw)
		}
	}
	if _, err := e.sched.CallDevelopment(t.Context(), run.ID, control.Principal{Kind: control.PrincipalRunAgent, RunID: "other"}, protocol.MethodDevArtifactList, json.RawMessage(`{}`), func() error { return nil }); !errors.Is(err, control.ErrInvalid) {
		t.Fatalf("foreign principal: %v", err)
	}
}
func TestDevelopmentArtifactDeleteReauthorizesAtEffect(t *testing.T) {
	e, run := developmentFixture(t)
	artifact := captureFixture(t, e.sched, run.ID)
	raw, _ := json.Marshal(protocol.DevArtifactDeleteParams{ArtifactID: artifact.ID})
	calls := 0
	_, err := e.sched.CallDevelopment(t.Context(), run.ID, control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}, protocol.MethodDevArtifactDelete, raw, func() error {
		calls++
		if calls >= 2 {
			return permissions.ErrDenied
		}
		return nil
	})
	if !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("delete after revoked authority: %v", err)
	}
	dir, err := e.sched.captureDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = readCapture(dir, run.ID, artifact.ID); err != nil {
		t.Fatalf("denied effect deleted capture: %v", err)
	}
}
func TestDevelopmentArtifactReaderFencesRevokedAccess(t *testing.T) {
	e, run := developmentFixture(t)
	artifact := captureFixture(t, e.sched, run.ID)
	allowed := true
	_, reader, err := e.sched.OpenDevelopmentArtifact(t.Context(), run.ID, control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}, artifact.ID, func() error {
		if !allowed {
			return permissions.ErrDenied
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Errorf("close artifact reader: %v", closeErr)
		}
	}()
	prefix := make([]byte, 8)
	if _, err = io.ReadFull(reader, prefix); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prefix, []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		t.Fatalf("not a PNG stream: %x", prefix)
	}
	allowed = false
	if n, readErr := reader.Read(prefix); n != 0 || !errors.Is(readErr, permissions.ErrDenied) {
		t.Fatalf("revoked read = %d,%v", n, readErr)
	}
	if _, _, err = e.sched.OpenDevelopmentArtifact(t.Context(), run.ID, control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}, "../../etc/passwd", func() error { return nil }); err == nil {
		t.Fatal("accepted host path as artifact handle")
	}
}
func TestDevelopmentRunCloseRequiresExplicitReopen(t *testing.T) {
	e, run := developmentFixture(t)
	live, err := e.sched.ResolveLiveRun(t.Context(), run.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.sched.closeDevelopmentAdmission(run.ID, live.ContainerID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.sched.ResolveLiveRun(t.Context(), run.ID, false); err == nil {
		t.Fatal("closed run admitted new development effects")
	}
	if _, err = e.sched.ResolveLiveRun(t.Context(), run.ID, true); err != nil {
		t.Fatalf("closed observations inaccessible: %v", err)
	}
	if err = e.sched.reopenDevelopment(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.sched.ResolveLiveRun(t.Context(), run.ID, false); err != nil {
		t.Fatalf("explicit reopen: %v", err)
	}
	if err = e.sched.Pause(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.sched.ResolveLiveRun(t.Context(), run.ID, false); err == nil {
		t.Fatal("paused run admitted effects")
	}
	if _, err = e.sched.ResolveLiveRun(t.Context(), run.ID, true); err != nil {
		t.Fatalf("paused observation: %v", err)
	}
}
func TestDevelopmentOnAFinishedRunSaysTheEnvironmentIsGone(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.Control = control.New(control.Config{}) })
	run, c := e.launchFake(t, "finish")
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	_, err := e.sched.CallDevelopment(t.Context(), run.ID, control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}, protocol.MethodDevBrowserStatus, json.RawMessage(`{"run_id":"`+string(run.ID)+`"}`), func() error { return nil })
	if !errors.Is(err, ErrNoLiveEnvironment) || err.Error() != "scheduler: the run has no live environment: the run is completed and its container is gone" {
		t.Fatalf("browser status on a finished run: %v", err)
	}
}
func TestDevelopmentCapturesAreBoundedAndRunScoped(t *testing.T) {
	e, run := developmentFixture(t)
	first := captureFixture(t, e.sched, run.ID)
	dir, err := e.sched.captureDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = readCapture(dir, "another-run", first.ID); err == nil {
		t.Fatal("cross-run capture metadata accepted")
	}
	for i := 1; i < maxDevelopmentCaptures; i++ {
		captureFixture(t, e.sched, run.ID)
	}
	before, err := listCaptures(dir, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	imageBytes, err := os.ReadFile(filepath.Join(dir, first.ID+".png"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.sched.saveDevelopmentCapture(t.Context(), run.ID, "browser", "", browser.Capture{Bytes: imageBytes, Metadata: browser.Metadata{ContentType: "image/png"}}, 0)
	if err == nil {
		t.Fatal("capture count capacity exceeded")
	}
	after, err := listCaptures(dir, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || after[0].ID != before[0].ID {
		t.Fatal("full store silently evicted existing capture")
	}
}
func TestDevelopmentBrowserCPUReservationRefusesOversubscription(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("Linux browser host admission")
	}
	e, _ := developmentFixture(t)
	d := e.sched.developmentState()
	for i := range goruntime.NumCPU() {
		d.reserved[domain.RunID("reserved-"+strconv.Itoa(i))] = true
	}
	if err := e.sched.reserveBrowser("one-too-many"); err == nil {
		t.Fatal("browser CPU capacity oversubscribed")
	}
	if d.reserved["one-too-many"] {
		t.Fatal("refused launch retained a new reservation")
	}
}
