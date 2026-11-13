package vm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func (h *harness) createStarted(key string, spec runtime.Spec) runtime.ID {
	h.t.Helper()
	id, err := h.engine.create(h.t.Context(), spec.CreationKey, spec, options{})
	if err != nil {
		h.t.Fatalf("create: %v", err)
	}
	if err := h.engine.start(h.t.Context(), key); err != nil {
		h.t.Fatalf("start: %v", err)
	}
	return id
}

func TestCreateRecordsWhatAContainerWouldReport(t *testing.T) {
	h := newHarness(t)
	spec := sessionSpec("run-a")
	spec.WorktreeHostPath, spec.WorktreeMountPath = t.TempDir(), "/workspace"
	spec.User = "1000:1000"
	id, err := h.engine.create(t.Context(), spec.CreationKey, spec, options{})
	if err != nil || id != "vm-run-a" {
		t.Fatalf("create = %q, %v", id, err)
	}
	info, err := h.engine.inspect(t.Context(), "run-a")
	if err != nil {
		t.Fatal(err)
	}
	if info.Image != "aether/standard:1" || info.User != "1000:1000" || info.Fingerprint != "fp-1" || info.Exited {
		t.Fatalf("inspect = %+v", info)
	}
	for _, want := range []string{"HOME=/root", "PATH=/usr/bin:/bin", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=/workspace"} {
		if !slices.Contains(info.Env, want) {
			t.Errorf("env %v lacks %s", info.Env, want)
		}
	}
	if !slices.Equal(info.MountTargets, []string{"/workspace"}) {
		t.Errorf("mount targets = %v", info.MountTargets)
	}
	rec, _ := h.engine.load("run-a")
	if rec.CPUs != 2 || rec.CPULimit != 1.5 || rec.MemoryMiB != 2048 || rec.WorkingDir != "/srv" {
		t.Errorf("record = %+v", rec)
	}
	if _, err := h.engine.create(t.Context(), spec.CreationKey, spec, options{}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second create = %v", err)
	}
	if found, err := h.engine.find("run-a"); err != nil || found != id {
		t.Errorf("find = %q, %v", found, err)
	}
	if _, err := h.engine.find("run-b"); !errors.Is(err, runtime.ErrNotFound) {
		t.Errorf("find missing = %v", err)
	}
}

func TestCreateRefusesAMissingImage(t *testing.T) {
	h := newHarness(t)
	missing := sessionSpec("run-m")
	missing.Image = "missing:1"
	if _, err := h.engine.create(t.Context(), missing.CreationKey, missing, options{}); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Errorf("missing image create = %v", err)
	}
	if _, err := os.Stat(h.engine.dir("run-m")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed create left %s: %v", h.engine.dir("run-m"), err)
	}
}

func TestStartBootsTheVMInItsUnit(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	spec := sessionSpec("run-a")
	spec.User, spec.Sudo, spec.NestedDocker = "1000:1000", true, true
	spec.SetupScript = "make deps"
	spec.Mounts = []runtime.Mount{{HostPath: home, ContainerPath: "/root"}}
	h.createStarted("run-a", spec)

	call := h.units.last()
	if call.name != "aether-vm-run-a.service" || call.argv[1] != MonitorCommand || call.argv[2] != h.engine.dir("run-a") {
		t.Fatalf("unit start = %+v", call)
	}
	for _, want := range []string{"PrivateMounts=yes", "CPUQuota=150%", "OOMScoreAdjust=500", "MemoryMax=2560M"} {
		if !slices.Contains(call.properties, want) {
			t.Errorf("properties %v lack %s", call.properties, want)
		}
	}
	args := strings.Join(call.launch.Args, " ")
	for _, want := range []string{
		"--cpus boot=2,nested=off", "--memory size=2048M,shared=on", "--balloon size=0,free_page_reporting=on",
		"--kernel /k/vmlinux", "page_reporting.page_reporting_order=0", "aether.mount=home,0,/root",
		"helper.img,image_type=raw,id=helper,serial=helper,readonly=on", "root.img,image_type=qcow2,id=root,serial=root,backing_files=on",
		"--net id=net0,tap=aethervm1,ip=10.200.0.5,mask=255.255.255.252",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("launch args lack %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "--fs") || len(call.launch.Shares) != 0 {
		t.Errorf("the home went over virtio-fs: %+v", call.launch)
	}
	if l := call.launch.Home; l == nil || len(l.Shares) != 1 || l.Shares[0].Source != home || l.UID != 1000 || l.GID != 1000 {
		t.Errorf("file service = %+v", call.launch.Home)
	}
	g := h.guest("run-a")
	if len(g.boots) != 1 {
		t.Fatalf("boots = %d", len(g.boots))
	}
	boot := g.boots[0]
	if boot.SetupScript != "make deps" || !boot.NestedDocker || boot.PidsLimit != 4096 || boot.Main.ID != guest.MainID || boot.Main.NoNewPrivileges ||
		boot.Main.User != "1000:1000" || boot.Main.Dir != "/srv" || boot.Main.TTY == nil || !slices.Equal(boot.Main.Argv, spec.Command) {
		t.Errorf("boot = %+v", boot)
	}
	var prepared bool
	for _, p := range g.spawned() {
		if p.User == "0:0" && slices.Contains(p.Argv, runtime.UserScript) {
			prepared = slices.Equal(p.Argv[len(p.Argv)-3:], []string{"1000:1000", "/root", "sudo"})
		}
	}
	if !prepared {
		t.Errorf("no root user prep among %+v", g.spawned())
	}
	if !h.engine.flag(h.engine.fresh, "run-a") || h.engine.settingUp("run-a") {
		t.Errorf("fresh/settingUp after start")
	}
	if err := h.engine.start(t.Context(), "run-a"); err != nil || len(h.units.started) != 1 || len(g.boots) != 2 {
		t.Errorf("redundant start: %v, %d unit starts, %d boots", err, len(h.units.started), len(g.boots))
	}
}

func TestStartReportsSetupFailureMaskedAndKillsTheVM(t *testing.T) {
	h := newHarness(t)
	spec := sessionSpec("run-a")
	spec.SecretEnv = []string{"TOKEN"}
	if _, err := h.engine.create(t.Context(), spec.CreationKey, spec, options{}); err != nil {
		t.Fatal(err)
	}
	h.guest("run-a").bootErr = &guest.SetupError{ExitCode: 3, Output: "using s3cret\nfailed\n"}
	err := h.engine.start(t.Context(), "run-a")
	var setup *runtime.SetupError
	if !errors.As(err, &setup) || setup.ExitCode != 3 || strings.Contains(setup.Output, "s3cret") || !strings.Contains(setup.Output, "using ***") {
		t.Fatalf("start = %v", err)
	}
	if h.units.vmm("run-a") != nil {
		t.Errorf("a failed start left the VM running")
	}
}

func TestStartFailsWithTheLogWhenTheVMDiesWhileBooting(t *testing.T) {
	h := newHarness(t)
	if _, err := h.engine.create(t.Context(), "run-a", sessionSpec("run-a"), options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.engine.path("run-a", monitorLog), []byte("cloud-hypervisor: Error booting VM: KvmNotAvailable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.units.failNext = true
	h.guest("run-a").pingErr = errors.New("connection refused")
	err := h.engine.start(t.Context(), "run-a")
	if err == nil || !strings.Contains(err.Error(), "stopped while booting") || !strings.Contains(err.Error(), "KvmNotAvailable") {
		t.Fatalf("start = %v", err)
	}
}

func TestACacheIsPromotedOnlyWhenItsVMStopsForGood(t *testing.T) {
	h := newHarness(t)
	h.images.promoted = make(chan string, 4)
	cache := runtime.Mount{HostPath: "/data/cache/m1/runs", ContainerPath: "/aether-cache", Kind: runtime.MountCache}
	want := cachePool(cache) + " cache.qcow2.snapshot"
	promoted := func() string {
		h.engine.promotions.Wait()
		select {
		case got := <-h.images.promoted:
			return got
		default:
			return ""
		}
	}
	session := func(key string, m runtime.Mount) {
		spec := sessionSpec(key)
		spec.Mounts = []runtime.Mount{m}
		h.createStarted(key, spec)
	}

	session("run-a", cache)
	if args := strings.Join(h.units.last().launch.Args, " "); !strings.Contains(args, "cache.qcow2,image_type=qcow2,id=cache,serial=cache") {
		t.Errorf("launch args lack the cache disk:\n%s", args)
	}
	if err := h.engine.save(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if got := promoted(); got != "" {
		t.Errorf("a save promoted %q", got)
	}
	if err := h.engine.restore(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if got := promoted(); got != want {
		t.Errorf("promoted %q after a clean stop, want %q", got, want)
	}
	if err := h.engine.destroy(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if got := promoted(); got != "" {
		t.Errorf("a deletion promoted the stopped VM's cache again: %q", got)
	}

	session("run-b", cache)
	if err := h.engine.save(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.destroy(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if got := promoted(); got != want {
		t.Errorf("promoted %q when a saved VM was deleted, want %q", got, want)
	}

	session("run-c", cache)
	h.guest("run-c").syncErr = errors.New("sync refused")
	if err := h.engine.save(t.Context(), "run-c"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.stop(t.Context(), "run-c", time.Second); err != nil {
		t.Fatal(err)
	}
	session("run-d", cache)
	h.guest("run-d").unresponsive = true
	if err := h.engine.stop(t.Context(), "run-d", time.Second); err != nil {
		t.Fatal(err)
	}
	session("run-e", cache)
	if err := h.engine.destroy(t.Context(), "run-e"); err != nil {
		t.Fatal(err)
	}
	isolated := cache
	isolated.Overlay = "/scratch/run-i/cache"
	session("run-i", isolated)
	if err := h.engine.stop(t.Context(), "run-i", time.Second); err != nil {
		t.Fatal(err)
	}
	if got := promoted(); got != "" {
		t.Errorf("promoted %q from an unsynced, killed or Isolated cache", got)
	}
}

// An account move destroys a session's VM and makes another under the same
// key: the session's database disk, outside the VM's directory, carries over.
func TestASessionsDatabaseDiskOutlivesItsVM(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	spec := sessionSpec("run-a")
	spec.Mounts = []runtime.Mount{
		{HostPath: dir, Subpath: "0", ContainerPath: "/root/.codex/vm-sqlite", Kind: runtime.MountState},
		{HostPath: dir, Subpath: "1", ContainerPath: "/root/.agent/index", Kind: runtime.MountState},
	}
	disk := filepath.Join(dir, stateDiskFile)
	h.createStarted("run-a", spec)
	args := strings.Join(h.units.last().launch.Args, " ")
	for _, want := range []string{"path=" + disk + ",image_type=raw,id=state,serial=state", "aether.mount=state,0,/root/.codex/vm-sqlite", "aether.mount=state,1,/root/.agent/index"} {
		if !strings.Contains(args, want) {
			t.Errorf("launch args lack %q:\n%s", want, args)
		}
	}
	if err := os.WriteFile(disk, []byte("databases"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.destroy(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if g := h.guest("run-a"); g.syncs != 1 {
		t.Errorf("the guest synced %d times before its VM was destroyed, want once", g.syncs)
	}
	h.createStarted("run-a", spec)
	if got, err := os.ReadFile(disk); err != nil || string(got) != "databases" {
		t.Errorf("the replacement VM's database disk holds %q, %v", got, err)
	}
	if args = strings.Join(h.units.last().launch.Args, " "); !strings.Contains(args, "serial=state") {
		t.Errorf("the replacement VM lacks the database disk:\n%s", args)
	}

	planted := sessionSpec("run-b")
	planted.Mounts = []runtime.Mount{{HostPath: t.TempDir(), Subpath: "0", ContainerPath: "/root/.codex/vm-sqlite", Kind: runtime.MountState}}
	if err := os.Symlink(disk, filepath.Join(planted.Mounts[0].HostPath, stateDiskFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.create(t.Context(), "run-b", planted, options{}); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Errorf("create over a symlinked database disk = %v", err)
	}
}

func TestRestoreServesTheSharesTheGuestMountedAtBoot(t *testing.T) {
	h := newHarness(t)
	memberHome, coord := t.TempDir(), t.TempDir()
	spec := sessionSpec("run-a")
	spec.Mounts = []runtime.Mount{{HostPath: memberHome, ContainerPath: "/root"}, {HostPath: coord, ContainerPath: "/run/aether", ReadOnly: true}}
	h.createStarted("run-a", spec)
	booted := h.units.last().launch.Home
	if err := h.engine.save(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(memberHome); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.restore(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if got := h.units.last().launch.Home; got == nil || !slices.Equal(got.Shares, booted.Shares) {
		t.Errorf("restore serves %+v, the guest mounted %+v", got, booted)
	}
}

func TestStopEndsMainRecordsItsExitAndPowersOff(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	if err := h.engine.pause(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.engine.exec(t.Context(), "run-a", []string{"true"}, ""); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Errorf("exec in a paused VM = %v", err)
	}
	vmm := h.units.vmm("run-a")
	if err := h.engine.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(vmm.called(), "vm.resume") {
		t.Errorf("stop did not resume the paused VM first: %v", vmm.called())
	}
	if h.units.vmm("run-a") != nil {
		t.Errorf("VM still running after stop")
	}
	info, err := h.engine.inspect(t.Context(), "run-a")
	if err != nil || !info.Exited || info.ExitCode != 128+int(syscall.SIGTERM) {
		t.Fatalf("inspect = %+v, %v", info, err)
	}
	if st, err := h.engine.wait(t.Context(), "run-a"); err != nil || st.Code != info.ExitCode {
		t.Errorf("wait = %+v, %v", st, err)
	}
	if err := h.engine.stop(t.Context(), "run-a", 0); err != nil {
		t.Errorf("stop of a stopped VM = %v", err)
	}
	if err := h.engine.start(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if h.images.roots() != 2 {
		t.Errorf("cold start used %d root disks, want a fresh one", h.images.roots())
	}
	if info, _ := h.engine.inspect(t.Context(), "run-a"); info.Exited {
		t.Errorf("restarted VM still reports exited")
	}
}

func TestStopKillsMainThatIgnoresTerm(t *testing.T) {
	h := newHarness(t)
	h.guest("run-a").ignoreTE = true
	h.createStarted("run-a", sessionSpec("run-a"))
	if err := h.engine.stop(t.Context(), "run-a", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if info, _ := h.engine.inspect(t.Context(), "run-a"); info.ExitCode != 128+int(syscall.SIGKILL) {
		t.Errorf("exit code = %d, want SIGKILL's", info.ExitCode)
	}
}

func TestWaitReportsMainsExitAndALostVM(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	main := h.guest("run-a").procs[guest.MainID]
	go func() {
		time.Sleep(50 * time.Millisecond)
		main.exit(7)
	}()
	st, err := h.engine.wait(t.Context(), "run-a")
	if err != nil || st.Code != 7 {
		t.Fatalf("wait = %+v, %v", st, err)
	}
	if h.units.vmm("run-a") != nil {
		t.Errorf("VM still running after Main exited")
	}

	h.createStarted("run-b", sessionSpec("run-b"))
	_ = h.units.Stop(t.Context(), unitName("run-b"))
	if st, err := h.engine.wait(t.Context(), "run-b"); err != nil || st.Code != lostExitCode {
		t.Fatalf("wait on a lost VM = %+v, %v", st, err)
	}
}

func TestAnotherEngineFindsAndDrivesARunningVM(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	after := h.newEngine(filepath.Dir(h.engine.root))
	id, err := after.find("run-a")
	if err != nil || id != "vm-run-a" {
		t.Fatalf("find after restart = %q, %v", id, err)
	}
	info, err := after.inspect(t.Context(), "run-a")
	if err != nil || info.Exited {
		t.Fatalf("inspect after restart = %+v, %v", info, err)
	}
	if pid, pidErr := after.initPID(t.Context(), "run-a"); pidErr != nil || pid != 4242 {
		t.Errorf("initPID = %d, %v", pid, pidErr)
	}
	if ip, ipErr := after.containerIP(t.Context(), "run-a"); ipErr != nil || ip != "10.200.0.6" {
		t.Errorf("containerIP = %q, %v", ip, ipErr)
	}
	h.guest("run-a").output = map[string]string{"echo": "hi\n"}
	code, out, _, err := after.exec(t.Context(), "run-a", []string{"echo", "hi"}, "")
	if err != nil || code != 0 || out != "hi\n" {
		t.Errorf("exec after restart = %d %q %v", code, out, err)
	}
	if err := after.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := after.destroy(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(after.dir("run-a")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destroy left %s", after.dir("run-a"))
	}
	if err := after.destroy(t.Context(), "run-a"); err != nil {
		t.Errorf("second destroy = %v", err)
	}
}

func TestExecRunsAsTheVMsUserWithItsEnvironment(t *testing.T) {
	h := newHarness(t)
	spec := sessionSpec("run-a")
	spec.User = "1000:1000"
	h.createStarted("run-a", spec)
	g := h.guest("run-a")
	g.output = map[string]string{"id": "uid=1000\n", "nope": "start-error:"}
	if code, out, _, err := h.engine.exec(t.Context(), "run-a", []string{"id"}, "/workspace"); err != nil || code != 0 || out != "uid=1000\n" {
		t.Fatalf("exec = %d %q %v", code, out, err)
	}
	var ran guest.ProcessSpec
	for _, p := range g.spawned() {
		if p.Argv[0] == "id" {
			ran = p
		}
	}
	if ran.User != "1000:1000" || ran.Dir != "/workspace" || !slices.Contains(ran.Env, "TOKEN=s3cret") || !slices.Contains(ran.Env, "PATH=/usr/bin:/bin") || !ran.NoNewPrivileges {
		t.Errorf("exec spawned %+v", ran)
	}
	if !g.boots[0].Main.NoNewPrivileges {
		t.Error("Main of a session without sudo may gain privileges")
	}
	if code, _, _, err := h.engine.exec(t.Context(), "run-a", []string{"nope"}, ""); err != nil || code != 127 {
		t.Errorf("exec of a missing command = %d, %v", code, err)
	}
	var exit *runtime.ExecExitError
	if _, err := h.engine.execTTY(t.Context(), "run-a", []string{"nope"}, "", 80, 24); !errors.As(err, &exit) || exit.Code != 127 {
		t.Errorf("execTTY of a missing command = %v", err)
	}
	g.output["big"] = strings.Repeat("x", runtime.ExecOutputLimit+1)
	if _, _, _, err := h.engine.exec(t.Context(), "run-a", []string{"big"}, ""); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("oversized exec = %v", err)
	}
	if env := execEnv(&record{Env: []string{"PATH=/home/agent/.local/bin:/usr/bin"}}, "0:0", nil); !slices.Equal(env, []string{runtime.RootPath}) {
		t.Errorf("root exec env = %v", env)
	}
}

func TestManagedExecGoesThroughDevExec(t *testing.T) {
	h := newHarness(t)
	spec := sessionSpec("run-a")
	spec.User = "1000:1000"
	id := h.createStarted("run-a", spec)
	m, err := h.engine.startManagedExec(t.Context(), "run-a", runtime.ExecSpec{Argv: []string{"bash"}, User: "0", CreationKey: "terminal-1", Cols: 80, Rows: 24}, true)
	if err != nil {
		t.Fatal(err)
	}
	identity := m.Identity()
	if identity.ContainerID != id || identity.CreationKey != "terminal-1" || identity.User != "0" {
		t.Errorf("identity = %+v", identity)
	}
	supervisor := h.guest("run-a").procs[identity.ExecID]
	if supervisor == nil || !slices.Equal(supervisor.spec.Argv[1:4], []string{"dev-exec", "run", "terminal-1"}) ||
		!slices.Contains(supervisor.spec.Env, "AETHER_DEVEXEC_CONTROL_GROUP=1000") {
		t.Fatalf("supervisor = %+v", supervisor)
	}
	if st, stErr := m.Status(t.Context()); stErr != nil || !st.Running || !st.Attached {
		t.Errorf("status = %+v, %v", st, stErr)
	}
	recovered, err := h.newEngine(filepath.Dir(h.engine.root)).recoverExec(t.Context(), "run-a", identity)
	if err != nil || recovered.Attachment() != nil {
		t.Fatalf("recover = %v, attachment %v", err, recovered)
	}
	st, err := recovered.Stop(t.Context(), time.Second)
	if err != nil || st.Code != 143 {
		t.Errorf("stop = %+v, %v", st, err)
	}
	if _, err := h.engine.recoverExec(t.Context(), "run-a", runtime.ExecIdentity{ExecID: "gone", CreationKey: "k", ClaimToken: "c"}); !errors.Is(err, runtime.ErrNotFound) {
		t.Errorf("recover of an unknown exec = %v", err)
	}
}

func TestSaveRefusesSharesAndRestoresAVMWithout(t *testing.T) {
	h := newHarness(t)
	shared := sessionSpec("run-s")
	shared.WorktreeHostPath, shared.WorktreeMountPath = t.TempDir(), "/workspace"
	h.createStarted("run-s", shared)
	if err := h.engine.save(t.Context(), "run-s"); err == nil || !strings.Contains(err.Error(), "over virtio-fs") {
		t.Fatalf("save with a share = %v", err)
	}
	if h.units.vmm("run-s") == nil {
		t.Error("a refused save stopped the VM")
	}

	h.createStarted("run-a", sessionSpec("run-a"))
	vmm := h.units.vmm("run-a")
	if err := h.engine.save(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(vmm.called(), []string{"vm.info", "vm.pause", "vm.snapshot", "vmm.shutdown"}) {
		t.Errorf("save calls = %v", vmm.called())
	}
	if _, err := os.Stat(h.engine.path("run-a", snapshotDir+"/state.json")); err != nil {
		t.Errorf("no saved state: %v", err)
	}
	if info, _ := h.engine.inspect(t.Context(), "run-a"); !info.Exited || info.ExitCode != killedExitCode {
		t.Errorf("a saved VM inspects as %+v, want stopped", info)
	}
	if st, err := h.engine.wait(t.Context(), "run-a"); err != nil || st.Code != killedExitCode {
		t.Errorf("wait on a saved VM = %+v, %v", st, err)
	}
	if err := h.engine.restore(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	restored := h.units.vmm("run-a")
	if !restored.restored || !slices.Contains(restored.called(), "vm.resume") || h.guest("run-a").clock.IsZero() {
		t.Errorf("restore: vmm %+v calls %v", restored, restored.called())
	}
	if _, err := os.Stat(h.engine.path("run-a", snapshotDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("snapshot kept after restore: %v", err)
	}
	if info, _ := h.engine.inspect(t.Context(), "run-a"); info.Exited {
		t.Errorf("a restored VM inspects as %+v", info)
	}
	waited := make(chan runtime.ExitStatus, 1)
	go func() {
		st, _ := h.engine.wait(context.WithoutCancel(t.Context()), "run-a")
		waited <- st
	}()
	h.guest("run-a").procs[guest.MainID].exit(0)
	if st := <-waited; st.Code != 0 {
		t.Errorf("wait after restore = %+v", st)
	}
	if err := h.engine.restore(t.Context(), "run-a"); err == nil || !strings.Contains(err.Error(), "no saved state") {
		t.Errorf("restore without state = %v", err)
	}

	h.createStarted("run-b", sessionSpec("run-b"))
	if err := h.engine.save(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	h.guest("run-b").clockErr = errors.New("clock refused")
	if err := h.engine.restore(t.Context(), "run-b"); err == nil {
		t.Fatal("restore with a failing clock succeeded")
	}
	rec, _ := h.engine.load("run-b")
	if rec.Saved || h.units.vmm("run-b") == nil {
		t.Errorf("a restore that failed after resume left saved=%v, running=%v; want the live VM kept", rec.Saved, h.units.vmm("run-b") != nil)
	}
	if _, err := os.Stat(h.engine.path("run-b", snapshotDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a resumed snapshot was kept: %v", err)
	}

	h.createStarted("run-c", sessionSpec("run-c"))
	if err := h.engine.pause(t.Context(), "run-c"); err != nil {
		t.Fatal(err)
	}
	paused := h.units.vmm("run-c")
	if err := h.engine.save(t.Context(), "run-c"); err != nil {
		t.Fatalf("save of a paused VM = %v", err)
	}
	if slices.Contains(paused.called()[1:], "vm.pause") {
		t.Errorf("save paused a paused VM again: %v", paused.called())
	}
	if err := h.engine.stop(t.Context(), "run-c", time.Second); err != nil {
		t.Fatal(err)
	}
	if rec, _ := h.engine.load("run-c"); rec.Saved || !rec.Exited {
		t.Errorf("stop of a saved VM left %+v", rec)
	}
	if _, err := os.Stat(h.engine.path("run-c", snapshotDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stop kept a saved VM's state: %v", err)
	}
}

func TestStatsAndMemoryEventsComeFromTheHelper(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	h.guest("run-a").stats = guest.Stats{CPUUsageNanos: 5, MemoryBytes: 300 << 20, MemoryLimitBytes: 2 << 30, Pids: 12, PidsLimit: 4096, OOMKills: 2}
	stats, err := h.engine.stats(t.Context())
	if err != nil || len(stats) != 1 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	st := stats[0]
	if st.ID != "vm-run-a" || st.CreationKey != "run-a" || st.CPULimitCores != 1.5 || st.MemoryBytes != 300<<20 || st.Pids != 12 ||
		st.NetRxBytes != 10 || st.NetTxBytes != 20 || st.DiskReadBytes != 4 || st.DiskWriteBytes != 6 {
		t.Errorf("stat = %+v", st)
	}
	events, err := h.engine.memoryEvents(t.Context(), "run-a")
	if err != nil || events.OOMKills != 2 || events.LimitBytes != 2<<30 {
		t.Errorf("memory events = %+v, %v", events, err)
	}
	if err := h.engine.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.memoryEvents(t.Context(), "run-a"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("memory events of a stopped VM = %v, want not-exist", err)
	}
}

func TestAVMWithACheckoutDiskLingersAfterMainExits(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	unlock := h.engine.lock("run-a")
	rec, err := h.engine.load("run-a")
	if err == nil {
		rec.CheckoutDisks = []checkoutDisk{{Target: "/workspace"}}
		err = h.engine.store(rec)
	}
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if h.units.vmm("run-a") == nil {
		t.Fatal("a VM with a checkout disk powered off at stop; it should linger for the server's git work")
	}
	if info, err := h.engine.inspect(t.Context(), "run-a"); err != nil || !info.Exited {
		t.Errorf("a lingering VM inspects as %+v, %v", info, err)
	}
	if _, _, _, err := h.engine.exec(t.Context(), "run-a", []string{"true"}, ""); err == nil {
		t.Error("exec in a VM whose main process exited succeeded")
	}
	h.engine.endLinger("run-a", rec.Boots, 0)
	deadline := time.Now().Add(5 * time.Second)
	for h.units.vmm("run-a") != nil {
		if time.Now().After(deadline) {
			t.Fatal("VM still running after its linger ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
