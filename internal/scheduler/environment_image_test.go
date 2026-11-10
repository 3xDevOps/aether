package scheduler

import (
	"archive/tar"
	"bytes"
	"errors"
	"slices"
	"strings"
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
}

func TestSavedImageSweepsKeepThePreviousImage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	var saved []string
	for range 3 {
		tag, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
		if err != nil {
			t.Fatalf("SaveEnvironment: %v", err)
		}
		saved = append(saved, tag)
	}
	repo := memberImageRepo(e.member.ID)
	want := []string{saved[2], saved[1]}
	if tags, _ := e.rt.ListImageTags(t.Context(), repo); !slices.Equal(tags, want) {
		t.Fatalf("images after three saves = %v, want %v", tags, want)
	}
	e.sched.sweepMemberImages(t.Context(), e.member.ID, "")
	if tags, _ := e.rt.ListImageTags(t.Context(), repo); !slices.Equal(tags, want) {
		t.Fatalf("images after maintenance = %v, want %v", tags, want)
	}
	if err := e.sched.ResetEnvironment(t.Context(), e.member.ID); err != nil {
		t.Fatalf("ResetEnvironment: %v", err)
	}
	if tags, _ := e.rt.ListImageTags(t.Context(), repo); len(tags) != 0 {
		t.Fatalf("images after reset = %v, want none", tags)
	}
}

func TestLaunchFallsBackFromUnstartableSavedImage(t *testing.T) {
	t.Parallel()
	const standard = "standard:latest"
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = standard })
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	var saved []string
	for range 2 {
		tag, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
		if err != nil {
			t.Fatalf("SaveEnvironment: %v", err)
		}
		saved = append(saved, tag)
	}
	previous, current := saved[0], saved[1]
	eoverflow := errors.New("OCI runtime create failed: runc create failed: unable to start container process: error during container init: rootfs_linux.go:76: setting up rootfs mounts caused: open usr/local/bin/sudo: value too large for defined data type")
	mkdirall := errors.New("OCI runtime create failed: rootfs_init_linux.go:637: failed to mkdirall /var/lib/sysbox/run/aether: permission denied")
	e.rt.mu.Lock()
	e.rt.imageStartErr = map[string]error{current: eoverflow}
	e.rt.mu.Unlock()

	launch := func() (*domain.Run, *fakeContainer) {
		t.Helper()
		member, err := e.db.GetMember(t.Context(), e.member.ID)
		if err != nil {
			t.Fatal(err)
		}
		e.member = member
		run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "build", "fake", domain.LaunchTUI)
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}
		fresh, err := e.db.GetRun(t.Context(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return fresh, e.rt.byName(string(run.ID))
	}
	run, c := launch()
	if c.spec.Image != previous {
		t.Fatalf("session image = %q, want the previous saved image %q", c.spec.Image, previous)
	}
	if run.Status != domain.RunRunning || !strings.Contains(run.Reason, "(value too large for defined data type), so this session uses your previous saved image "+previous) || !strings.Contains(run.Reason, "aether env reset") {
		t.Fatalf("session = %s %q, want running with the fallback notice", run.Status, run.Reason)
	}

	e.rt.mu.Lock()
	e.rt.imageStartErr[previous] = mkdirall
	e.rt.mu.Unlock()
	run, c = launch()
	if c.spec.Image != standard {
		t.Fatalf("session image = %q, want the standard image", c.spec.Image)
	}
	if !strings.Contains(run.Reason, "(value too large for defined data type), so this session uses the standard image") {
		t.Fatalf("session reason = %q, want the standard-image notice", run.Reason)
	}

	e.rt.mu.Lock()
	e.rt.imageStartErr[standard] = eoverflow
	e.rt.mu.Unlock()
	member, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	creates := len(e.rt.containers)
	if _, err := e.sched.Launch(t.Context(), e.ws.ID, member.ID, member.ID, "build", "fake", domain.LaunchTUI); err == nil || !strings.Contains(err.Error(), "value too large") {
		t.Fatalf("Launch with every image unstartable = %v, want the runtime's error", err)
	}
	if got := len(e.rt.containers) - creates; got > 1 {
		t.Fatalf("launch created %d containers after the standard image failed, want at most one attempt", got)
	}
}

func TestUnstartableImageCause(t *testing.T) {
	t.Parallel()
	for msg, want := range map[string]string{
		"start container: rootfs_linux.go:76: open usr/local/bin/sudo: value too large for defined data type": "value too large for defined data type",
		"start container: rootfs_init_linux.go:637: failed to mkdirall /run/aether: permission denied":        "root filesystem: permission denied",
		"create container: no such image":                                                         "",
		"start terminal: value too large for defined data type":                                   "",
		"start container: open /dev/null: permission denied":                                      "",
		"start container: error mounting \"/srv/home\" to rootfs at \"/root\": permission denied": "",
	} {
		if got := unstartableImageCause(errors.New(msg)); got != want {
			t.Errorf("unstartableImageCause(%q) = %q, want %q", msg, got, want)
		}
	}
}
