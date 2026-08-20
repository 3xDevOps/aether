package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type setupImageRuntime struct {
	runtime.Runtime

	mu       sync.Mutex
	user     string
	users    map[string]string
	imageRef string
}

func (r *setupImageRuntime) ImageUser(_ context.Context, ref string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.imageRef = ref
	if user, ok := r.users[ref]; ok {
		return user, nil
	}
	return r.user, nil
}

func (r *setupImageRuntime) resolvedImage() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.imageRef
}

func TestSetupLoginCleansContainer(t *testing.T) {
	dir := t.TempDir()
	e := newTestEnv(t, func(c *Config) {
		c.HomesDir = filepath.Join(dir, "homes")
	})
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- e.sched.SetupLogin(t.Context(), e.member.ID, "claude", "", 80, 24, a, nil)
	}()
	waitFor(t, "setup container", func() bool {
		return e.rt.byName("setup-"+string(e.member.ID)+"-claude") != nil
	})
	c := e.rt.byName("setup-" + string(e.member.ID) + "-claude")
	if c.spec.User != "" {
		t.Errorf("user = %q, want empty (root default)", c.spec.User)
	}
	if c.spec.Env["HOME"] != "/root" {
		t.Errorf("HOME = %q, want /root", c.spec.Env["HOME"])
	}
	if got, want := strings.Join(c.spec.Command, " "), "/bin/sh -i"; got != want {
		t.Errorf("setup command = %q, want %q", got, want)
	}
	if got, want := c.spec.Env["PS1"], "aether-setup$ "; got != want {
		t.Errorf("setup PS1 = %q, want %q", got, want)
	}
	if len(c.spec.Mounts) != 1 || c.spec.Mounts[0].ContainerPath != "/root/.claude" || c.spec.Mounts[0].ReadOnly {
		t.Errorf("credential mounts = %+v, want writable /root/.claude target", c.spec.Mounts)
	}
	_ = b.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetupLogin: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetupLogin did not return")
	}
}

// setupHalfCloseConn wraps one end of a net.Pipe and records whether
// SetupLogin half-closed it when the container's output ended. Providing
// CloseWrite mirrors the SSH-backed conn used in production; a full Close
// would drop the exit status the subsystem handler still has to send.
type setupHalfCloseConn struct {
	net.Conn
	mu         sync.Mutex
	halfClosed bool
}

func (c *setupHalfCloseConn) CloseWrite() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.halfClosed = true
	return nil
}

func (c *setupHalfCloseConn) wasHalfClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.halfClosed
}

func TestSetupLoginReturnsWhenContainerExits(t *testing.T) {
	e := newTestEnv(t, nil)
	a, b := net.Pipe()
	defer func() {
		_ = a.Close()
		_ = b.Close()
	}()
	conn := &setupHalfCloseConn{Conn: a}
	done := make(chan error, 1)
	go func() {
		done <- e.sched.SetupLogin(t.Context(), e.member.ID, "claude", "", 80, 24, conn, nil)
	}()
	waitFor(t, "setup container", func() bool {
		return e.rt.byName("setup-"+string(e.member.ID)+"-claude") != nil
	})
	e.rt.byName("setup-" + string(e.member.ID) + "-claude").exitNow(0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetupLogin: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetupLogin did not return after the container exited")
	}
	if !conn.wasHalfClosed() {
		t.Error("SetupLogin did not half-close the client conn after container exit")
	}
}

func TestSetupLoginUsesImageNumericUser(t *testing.T) {
	dir := t.TempDir()
	user := ownableNonRootUser(t, dir)

	reposFile := filepath.Join(dir, "not-a-repos-directory")
	if err := os.WriteFile(reposFile, []byte("block accidental repo access"), 0o600); err != nil {
		t.Fatalf("write repos blocker: %v", err)
	}
	e := newTestEnv(t, func(c *Config) {
		c.HomesDir = filepath.Join(dir, "homes")
		c.ReposDir = reposFile
		c.Runtime = &setupImageRuntime{Runtime: c.Runtime, user: user}
	})
	registerImage(t, e, "non-root-image")
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- e.sched.SetupLogin(t.Context(), e.member.ID, "claude", "non-root-image", 80, 24, a, nil)
	}()

	name := "setup-" + string(e.member.ID) + "-claude"
	waitFor(t, "non-root setup container", func() bool {
		return e.rt.byName(name) != nil
	})
	resolver := e.cfg.Runtime.(*setupImageRuntime)
	if got := resolver.resolvedImage(); got != "non-root-image" {
		t.Errorf("resolved image = %q, want non-root-image", got)
	}
	c := e.rt.byName(name)
	if c.spec.User != user {
		t.Errorf("user = %q, want %s", c.spec.User, user)
	}
	if c.spec.Env["HOME"] != "/home/aether" {
		t.Errorf("HOME = %q, want /home/aether", c.spec.Env["HOME"])
	}
	if len(c.spec.Mounts) != 1 || c.spec.Mounts[0].ContainerPath != "/home/aether/.claude" || c.spec.Mounts[0].ReadOnly {
		t.Errorf("credential mounts = %+v, want writable /home/aether/.claude target", c.spec.Mounts)
	}

	_ = b.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetupLogin: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetupLogin did not return")
	}
}

func TestSetupLoginRejectsUserConflictingWithLiveRun(t *testing.T) {
	dir := t.TempDir()
	e := newTestEnv(t, func(c *Config) {
		c.HomesDir = filepath.Join(dir, "homes")
		c.Runtime = &setupImageRuntime{Runtime: c.Runtime, user: "2000:2000"}
	})
	registerImage(t, e, "conflicting-image")
	live := &supervised{
		runID:    "run-live",
		memberID: e.member.ID,
		harness:  "claude",
		runUser:  "1000:1000",
	}
	e.sched.mu.Lock()
	e.sched.runs[live.runID] = live
	e.sched.mu.Unlock()

	err := e.sched.SetupLogin(t.Context(), e.member.ID, "claude", "conflicting-image", 80, 24, &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("setup with conflicting uid:gid was accepted")
	}
	for _, want := range []string{"run-live", "1000:1000", "2000:2000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict error %q does not name %q", err, want)
		}
	}
	if c := e.rt.byName("setup-" + string(e.member.ID) + "-claude"); c != nil {
		t.Error("conflicting setup created a container")
	}
}

func TestSetupLoginReservationConflictsAndReleases(t *testing.T) {
	dir := t.TempDir()
	user := ownableNonRootUser(t, dir)
	conflictingUser := "1:1"
	if user == conflictingUser {
		conflictingUser = "2:2"
	}
	e := newTestEnv(t, func(c *Config) {
		c.HomesDir = filepath.Join(dir, "homes")
		c.Runtime = &setupImageRuntime{
			Runtime: c.Runtime,
			users: map[string]string{
				"first-image":       user,
				"conflicting-image": conflictingUser,
			},
		}
	})
	registerImage(t, e, "first-image")
	registerImage(t, e, "conflicting-image")

	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- e.sched.SetupLogin(t.Context(), e.member.ID, "claude", "first-image", 80, 24, a, nil)
	}()
	waitFor(t, "first setup container", func() bool {
		return e.rt.byName("setup-"+string(e.member.ID)+"-claude") != nil
	})
	if got := credentialUserReservationCount(e.sched); got != 1 {
		t.Fatalf("live setup reservations = %d, want 1", got)
	}

	err := e.sched.SetupLogin(t.Context(), e.member.ID, "claude", "conflicting-image", 80, 24, &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("concurrent setup with conflicting uid:gid was accepted")
	}
	for _, want := range []string{user, conflictingUser} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict error %q does not name %q", err, want)
		}
	}

	_ = b.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first SetupLogin: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first SetupLogin did not return")
	}
	if got := credentialUserReservationCount(e.sched); got != 0 {
		t.Errorf("setup reservations after session end = %d, want 0", got)
	}
}

// registerImage makes image selectable by SetupLogin: only an image an
// admin registered on a workspace may be started.
func registerImage(t *testing.T, e *testEnv, image string) {
	t.Helper()
	ws := &domain.Workspace{Name: "ws-" + image, Image: image}
	if err := e.db.CreateWorkspace(t.Context(), ws); err != nil {
		t.Fatalf("create workspace for %s: %v", image, err)
	}
}

func ownableNonRootUser(t *testing.T, dir string) string {
	t.Helper()
	if goruntime.GOOS != "linux" {
		t.Skip("non-root container ownership requires Linux")
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid, gid = 1, 1
	}
	probe := filepath.Join(dir, "ownership-probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatalf("write ownership probe: %v", err)
	}
	if err := os.Lchown(probe, uid, gid); err != nil {
		t.Skipf("filesystem cannot assign a non-root container identity: %v", err)
	}
	return fmt.Sprintf("%d:%d", uid, gid)
}

func credentialUserReservationCount(s *Scheduler) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.credentialUsers)
}

// The setup image is a selector over the workspaces that already exist,
// never a value: an arbitrary client-supplied image must not be pulled or
// run.
func TestSetupLoginRejectsUnknownImage(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.HomesDir = filepath.Join(t.TempDir(), "homes") })
	err := e.sched.SetupLogin(t.Context(), e.member.ID, "claude", "evil/backdoor:latest", 80, 24, &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("setup accepted an image matching no workspace")
	}
	if !strings.Contains(err.Error(), "evil/backdoor:latest") {
		t.Errorf("error %q does not name the rejected image", err)
	}
	if c := e.rt.byName("setup-" + string(e.member.ID) + "-claude"); c != nil {
		t.Error("rejected setup created a container")
	}
}
