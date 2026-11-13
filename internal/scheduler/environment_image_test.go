package scheduler

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type tarEntry struct {
	name     string
	uid, gid int
}

func imageTar(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Uid: e.uid, Gid: e.gid, Mode: 0o644, Typeflag: tar.TypeReg}
		if strings.HasSuffix(e.name, "/") {
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var shiftedImage = []tarEntry{
	{name: "etc/", uid: 231072, gid: 231072},
	{name: "etc/passwd", uid: 231072, gid: 231072},
	{name: "etc/hostname"},
	{name: "home/dev/", uid: 1000, gid: 1000},
	{name: "nobody", uid: 65534, gid: 65534},
	{name: "run/mount/", gid: 4294736224},
	{name: "usr/", uid: 231072, gid: 231072},
}

func TestCheckImageOwnersRejectsUnmappedOwners(t *testing.T) {
	t.Parallel()
	err := checkImageOwners(bytes.NewReader(imageTar(t, shiftedImage...)))
	bad, ok := errors.AsType[*errImageOwners](err)
	if !ok || bad.total != 4 {
		t.Fatalf("checkImageOwners = %v, want 4 unmapped paths", err)
	}
	want := "4 paths are owned by a uid or gid above 65535, which no container can map (3 owned by 231072:231072, 1 owned by 0:4294736224; for example /etc, /etc/passwd, /run/mount)"
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  %q", err, want)
	}
	if err := checkImageOwners(bytes.NewReader(imageTar(t, shiftedImage[2:5]...))); err != nil {
		t.Fatalf("checkImageOwners on mappable owners = %v", err)
	}
	if err := checkImageOwners(strings.NewReader("not a tar archive at all, but long enough to be read as a header block")); err == nil {
		t.Fatal("checkImageOwners accepted a corrupt archive")
	}
}

func TestSaveEnvironmentRejectsImageWithUnmappedOwners(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	good, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("first SaveEnvironment: %v", err)
	}
	e.rt.mu.Lock()
	e.rt.exports = map[runtime.ID][]byte{runtime.ID(terminal.ContainerID): imageTar(t, shiftedImage...)}
	e.rt.mu.Unlock()
	_, err = e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err == nil {
		t.Fatal("SaveEnvironment accepted an image with unmapped owners")
	}
	for _, want := range []string{"nothing was saved", "would not start", "3 owned by 231072:231072", "/etc/passwd", "unsaved changes are kept", "your saved image " + good} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err, want)
		}
	}
	member, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil || member.Image != good {
		t.Fatalf("member image = %q, %v, want the previous %q", member.Image, err, good)
	}
	if tags, _ := e.rt.ListImageTags(t.Context(), memberImageRepo(e.member.ID)); !slices.Equal(tags, []string{good}) {
		t.Fatalf("member images = %v, want only %q", tags, good)
	}
	if c, getErr := e.rt.get(runtime.ID(terminal.ContainerID)); getErr != nil || c.currentState() != "running" {
		t.Fatalf("terminal after a rejected save = %v, want it running", getErr)
	}

	e.rt.mu.Lock()
	e.rt.exportErr = errors.New("export refused")
	e.rt.mu.Unlock()
	if _, err = e.sched.SaveEnvironment(t.Context(), e.member.ID); err == nil || !strings.Contains(err.Error(), "could not be checked: export refused") {
		t.Fatalf("SaveEnvironment with a failed check = %v, want the check's error", err)
	}
	if tags, _ := e.rt.ListImageTags(t.Context(), memberImageRepo(e.member.ID)); !slices.Equal(tags, []string{good}) {
		t.Fatalf("member images after a failed check = %v, want only %q", tags, good)
	}
	if pending, _ := e.rt.ListImageTags(t.Context(), pendingImageRepo(e.member.ID)); len(pending) != 0 {
		t.Fatalf("pending images after rejected saves = %v, want none", pending)
	}

	// A rejected image whose removal failed never becomes the previous one.
	left := pendingImageRepo(e.member.ID) + ":left"
	if err = e.rt.Commit(t.Context(), runtime.ID(terminal.ContainerID), left, nil); err != nil {
		t.Fatal(err)
	}
	e.rt.holdImage(left, true)
	e.rt.mu.Lock()
	e.rt.exports, e.rt.exportErr = nil, nil
	e.rt.mu.Unlock()
	next, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("SaveEnvironment after rejections: %v", err)
	}
	if tags := e.memberImages(t); !slices.Equal(tags, slices.Sorted(slices.Values([]string{next, good}))) {
		t.Fatalf("member images = %v, want %q and the previous %q", tags, next, good)
	}
	e.rt.holdImage(left, false)
	e.sched.sweepMemberImages(t.Context(), e.member.ID, "")
	if pending, _ := e.rt.ListImageTags(t.Context(), pendingImageRepo(e.member.ID)); len(pending) != 0 {
		t.Fatalf("pending images after maintenance = %v, want none", pending)
	}
}

func TestCheckImageOwnersCountsAtMostThreeOwners(t *testing.T) {
	t.Parallel()
	var entries []tarEntry
	for i := range 5 {
		entries = append(entries, tarEntry{name: "f" + string(rune('a'+i)), uid: 70000 + i})
	}
	err := checkImageOwners(bytes.NewReader(imageTar(t, entries...)))
	bad, ok := errors.AsType[*errImageOwners](err)
	if !ok || bad.total != 5 || len(bad.owners) != 3 || !strings.Contains(err.Error(), "2 by other owners") {
		t.Fatalf("checkImageOwners = %v, want 5 paths, 3 owners counted and 2 by other owners", err)
	}
}

func (e *testEnv) memberImages(t *testing.T) []string {
	t.Helper()
	tags, err := e.rt.ListImageTags(t.Context(), memberImageRepo(e.member.ID))
	if err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(slices.Values(tags))
}

func (e *testEnv) saveTimes(t *testing.T, n int) []string {
	t.Helper()
	var saved []string
	for range n {
		tag, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
		if err != nil {
			t.Fatalf("SaveEnvironment: %v", err)
		}
		saved = append(saved, tag)
	}
	return saved
}

func TestSavedImageSweepsKeepThePreviousImage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	saved := e.saveTimes(t, 3)
	want := slices.Sorted(slices.Values(saved[1:]))
	if tags := e.memberImages(t); !slices.Equal(tags, want) {
		t.Fatalf("images after three saves = %v, want %v", tags, want)
	}
	if previous, err := e.sched.previousImage(t.Context(), saved[2]); err != nil || previous != saved[1] {
		t.Fatalf("previous of the third save = %q, %v, want %q", previous, err, saved[1])
	}
	e.sched.sweepMemberImages(t.Context(), e.member.ID, "")
	if tags := e.memberImages(t); !slices.Equal(tags, want) {
		t.Fatalf("images after maintenance = %v, want %v", tags, want)
	}

	// A session still holds the previous image when the member resets: it
	// stays on disk, but never becomes the previous image of a later save.
	e.rt.holdImage(saved[1], true)
	if err := e.sched.ResetEnvironment(t.Context(), e.member.ID); err != nil {
		t.Fatalf("ResetEnvironment: %v", err)
	}
	if tags := e.memberImages(t); !slices.Equal(tags, []string{saved[1]}) {
		t.Fatalf("images after reset = %v, want only the held %q", tags, saved[1])
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal after reset: %v", err)
	}
	fresh := e.saveTimes(t, 1)[0]
	if previous, err := e.sched.previousImage(t.Context(), fresh); err != nil || previous != "" {
		t.Fatalf("previous of the first save after reset = %q, %v, want none", previous, err)
	}
	e.rt.holdImage(saved[1], false)
	e.sched.sweepMemberImages(t.Context(), e.member.ID, "")
	if tags := e.memberImages(t); !slices.Equal(tags, []string{fresh}) {
		t.Fatalf("images after the hold ended = %v, want only %q", tags, fresh)
	}
}

func TestSaveEnvironmentReleasesTheCacheLockDuringTheCheck(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	locked := true
	e.rt.exportHook = func() {
		if unlock, ok := e.sched.cfg.Homes.TryLockCaches(e.member.ID); ok {
			locked = false
			unlock()
		}
	}
	e.saveTimes(t, 1)
	if locked {
		t.Fatal("the member's cache lock was held while the saved image was checked")
	}
}

func TestSweepRemovesALeftoverImageCheckContainer(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	id := runtime.ExportContainer(pendingImageRepo(e.member.ID))
	e.rt.mu.Lock()
	e.rt.containers[id] = &fakeContainer{id: id, state: "created", done: make(chan struct{})}
	e.rt.mu.Unlock()
	e.sched.sweepMemberImages(t.Context(), e.member.ID, "")
	if _, err := e.rt.get(id); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("check container after the sweep = %v, want it removed", err)
	}
}

const (
	eoverflowErr = "OCI runtime create failed: runc create failed: unable to start container process: error during container init: rootfs_linux.go:76: setting up rootfs mounts caused: open usr/local/bin/sudo: value too large for defined data type"
	mkdirallErr  = "OCI runtime create failed: rootfs_init_linux.go:637: failed to mkdirall /var/lib/sysbox/rootfs/run/aether: permission denied"
)

func (e *testEnv) failImageStarts(errs map[string]string) {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	if e.rt.imageStartErr == nil {
		e.rt.imageStartErr = make(map[string]error)
	}
	for image, msg := range errs {
		e.rt.imageStartErr[image] = errors.New(msg)
	}
}

func (e *testEnv) launchFromSaved(t *testing.T) (*domain.Run, *fakeContainer, error) {
	t.Helper()
	member, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := e.sched.Launch(t.Context(), e.ws.ID, member.ID, member.ID, "build", "fake", domain.LaunchTUI)
	if err != nil {
		return nil, nil, err
	}
	fresh, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fresh, e.rt.byName(string(run.ID)), nil
}

func (e *testEnv) unstartableMarks() map[string]string {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	return maps.Clone(e.sched.unstartableImages)
}

func TestLaunchFallsBackFromUnstartableSavedImage(t *testing.T) {
	t.Parallel()
	const standard = "standard:latest"
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = standard })
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	saved := e.saveTimes(t, 2)
	previous, current := saved[0], saved[1]
	_, previousName, _ := strings.Cut(previous, ":")
	_, currentName, _ := strings.Cut(current, ":")
	e.failImageStarts(map[string]string{current: eoverflowErr})

	// Concurrent launches each fall back, whether or not another one has
	// already marked the image.
	var wg sync.WaitGroup
	runs := make([]*fakeContainer, 3)
	errs := make([]error, 3)
	for i := range runs {
		wg.Go(func() { _, runs[i], errs[i] = e.launchFromSaved(t) })
	}
	wg.Wait()
	for i, c := range runs {
		if errs[i] != nil || c.spec.Image != previous {
			t.Fatalf("concurrent launch %d = %v, image %+v, want the previous saved image %q", i, errs[i], c, previous)
		}
	}
	run, c, err := e.launchFromSaved(t)
	if err != nil || c.spec.Image != previous {
		t.Fatalf("launch = %v, want it on the previous image", err)
	}
	want := "Saved image " + currentName + " cannot start (value too large for defined data type); this session uses your previous saved image " + previousName + ". Save your environment again to replace it."
	if run.Status != domain.RunRunning || run.Reason != want {
		t.Fatalf("session = %s %q\nwant running with %q", run.Status, run.Reason, want)
	}

	e.failImageStarts(map[string]string{previous: mkdirallErr})
	run, c, err = e.launchFromSaved(t)
	if err != nil || c.spec.Image != standard {
		t.Fatalf("launch = %v, want it on the standard image", err)
	}
	want = "Saved images " + currentName + " and " + previousName + " cannot start (value too large for defined data type; rootfs permission denied); this session uses the standard image. Save your environment again to replace them."
	if run.Reason != want || len([]rune(run.Reason)) > maxPublicRunStatusReason {
		t.Fatalf("session reason = %q\nwant %q", run.Reason, want)
	}

	// The standard image fails the same way: the images were not the cause.
	e.failImageStarts(map[string]string{standard: eoverflowErr})
	e.rt.mu.Lock()
	creates := e.rt.seq
	e.rt.mu.Unlock()
	if _, _, err := e.launchFromSaved(t); err == nil || !strings.Contains(err.Error(), "value too large for defined data type") {
		t.Fatalf("launch with every image failing = %v, want the runtime's error", err)
	}
	e.rt.mu.Lock()
	attempts := e.rt.seq - creates
	e.rt.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("launch made %d attempts, want one on the standard image", attempts)
	}
	if marks := e.unstartableMarks(); len(marks) != 0 {
		t.Fatalf("marks after the standard image failed too = %v, want none", marks)
	}
}

func TestLaunchDoesNotBlameTheImageForAMountFailure(t *testing.T) {
	t.Parallel()
	const standard = "standard:latest"
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = standard })
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	saved := e.saveTimes(t, 2)

	// A mount target under the bind-mounted home fails on every image.
	nested := "OCI runtime create failed: rootfs_init_linux.go:637: failed to mkdirall /var/lib/sysbox/rootfs/root/.claude/projects: permission denied"
	e.failImageStarts(map[string]string{saved[0]: nested, saved[1]: nested, standard: nested})
	e.rt.mu.Lock()
	creates := e.rt.seq
	e.rt.mu.Unlock()
	if _, _, err := e.launchFromSaved(t); err == nil || !strings.Contains(err.Error(), "/root/.claude/projects: permission denied") {
		t.Fatalf("launch = %v, want the runtime's error", err)
	}
	e.rt.mu.Lock()
	attempts := e.rt.seq - creates
	e.rt.mu.Unlock()
	if attempts != 1 || len(e.unstartableMarks()) != 0 {
		t.Fatalf("attempts = %d, marks = %v, want one attempt and no marks", attempts, e.unstartableMarks())
	}

	// The same error on the image's own directories, but on every image.
	e.failImageStarts(map[string]string{saved[0]: mkdirallErr, saved[1]: mkdirallErr, standard: mkdirallErr})
	if _, _, err := e.launchFromSaved(t); err == nil || !strings.Contains(err.Error(), "run/aether: permission denied") {
		t.Fatalf("launch = %v, want the runtime's error", err)
	}
	if marks := e.unstartableMarks(); len(marks) != 0 {
		t.Fatalf("marks = %v, want none: the standard image failed the same way", marks)
	}
}

func TestTerminalFallsBackFromUnstartableSavedImage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	saved := e.saveTimes(t, 2)
	if err := e.sched.StopTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("StopTerminal: %v", err)
	}
	if destroyed, err := e.sched.DestroyStoppedTerminal(t.Context(), e.member.ID); err != nil || !destroyed {
		t.Fatalf("DestroyStoppedTerminal = %v, %v", destroyed, err)
	}
	e.failImageStarts(map[string]string{saved[1]: eoverflowErr})
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil || terminal.Image != saved[0] {
		t.Fatalf("EnsureTerminal = %+v, %v, want it on the previous image %q", terminal, err, saved[0])
	}
	if marks := e.unstartableMarks(); marks[saved[1]] == "" {
		t.Fatalf("marks = %v, want the saved image marked", marks)
	}
	// Saving from it records the image it runs, not the broken one, as the
	// previous image.
	tag := e.saveTimes(t, 1)[0]
	if previous, err := e.sched.previousImage(t.Context(), tag); err != nil || previous != saved[0] {
		t.Fatalf("previous of the new save = %q, %v, want %q", previous, err, saved[0])
	}
}

func TestUnstartableImageCause(t *testing.T) {
	t.Parallel()
	mounts := []string{"/workspace", "/root", "/run/aether"}
	for msg, want := range map[string]string{
		"start container: rootfs_linux.go:76: open usr/local/bin/sudo: value too large for defined data type":            "value too large for defined data type",
		"start container: rootfs_init_linux.go:637: failed to mkdirall /rootfs/run/aether: permission denied":            "rootfs permission denied",
		"scheduler: start terminal: rootfs_linux.go:76: value too large for defined data type":                           "value too large for defined data type",
		"start container: rootfs_init_linux.go:637: failed to mkdirall /rootfs/root/.claude/projects: permission denied": "",
		"create container: no such image":                                                         "",
		"start terminal: value too large for defined data type":                                   "",
		"start container: open /dev/null: permission denied":                                      "",
		"start container: error mounting \"/srv/home\" to rootfs at \"/root\": permission denied": "",
	} {
		if got := unstartableImageCause(errors.New(msg), mounts); got != want {
			t.Errorf("unstartableImageCause(%q) = %q, want %q", msg, got, want)
		}
	}
	vm := fmt.Errorf("start container: vm runtime: VM run-a failed to boot: switch to the root disk: mount /dev/vdb: %w", runtime.ErrUnstartableImage)
	if got := unstartableImageCause(vm, mounts); got != "it does not boot" {
		t.Errorf("unstartableImageCause(VM boot failure) = %q", got)
	}
	setup := fmt.Errorf("start container: %w", &runtime.SetupError{ExitCode: 125, Output: "docker: value too large for defined data type"})
	if got := unstartableImageCause(setup, mounts); got != "" {
		t.Errorf("unstartableImageCause(setup failure) = %q, want none", got)
	}
}

func TestSaveAfterRestartRecordsTheTerminalsFallbackImage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	saved := e.saveTimes(t, 2)
	working, broken := saved[0], saved[1]
	if err := e.sched.StopTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("StopTerminal: %v", err)
	}
	if destroyed, err := e.sched.DestroyStoppedTerminal(t.Context(), e.member.ID); err != nil || !destroyed {
		t.Fatalf("DestroyStoppedTerminal = %v, %v", destroyed, err)
	}
	e.failImageStarts(map[string]string{broken: eoverflowErr})
	if terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil || terminal.Image != working {
		t.Fatalf("EnsureTerminal = %+v, %v, want it on %q", terminal, err, working)
	}

	// The restart forgets the marks; the adopted terminal still runs the
	// working image while the member's image is the broken one.
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	recovered := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, recovered)
	waitFor(t, "terminal recovery", func() bool { return recovered.lookupTerminal(e.member.ID) != nil })
	if _, err := recovered.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal after restart: %v", err)
	}
	recovered.mu.Lock()
	marks := len(recovered.unstartableImages)
	recovered.mu.Unlock()
	if member, err := e.db.GetMember(t.Context(), e.member.ID); err != nil || member.Image != broken || marks != 0 {
		t.Fatalf("member image = %q, %d marks, %v; want %q and no marks", member.Image, marks, err, broken)
	}
	tag, err := recovered.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}
	if previous, err := recovered.previousImage(t.Context(), tag); err != nil || previous != working {
		t.Fatalf("previous of the save = %q, %v, want the terminal's %q", previous, err, working)
	}
	if tags := e.memberImages(t); !slices.Equal(tags, slices.Sorted(slices.Values([]string{tag, working}))) {
		t.Fatalf("images after the save = %v, want %q and %q, without the broken %q", tags, tag, working, broken)
	}
}
