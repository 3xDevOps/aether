//go:build kvm

package vm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestKVMClosedSessionStartsAgainFromItsDisks closes a session VM the way
// the scheduler does, a freeze and then a stop that keeps only its disks,
// and starts it again with another command, first fresh and then after it
// installed packages and built a program: the root disk still holds them,
// the setup script and the launch command ran once, and the new command
// runs. It logs the disk a closed session keeps and how long the start
// takes.
func TestKVMClosedSessionStartsAgainFromItsDisks(t *testing.T) {
	e := kvmEngine(t)
	const key = "run-vmreinstate"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, TTY: true,
		Command:     []string{"/bin/sh", "-c", "echo launched >> /var/tmp/launches; echo task-started; exec sleep infinity"},
		SetupScript: "echo setup >> /var/tmp/setups",
		Env:         map[string]string{"AETHER_RUN_ID": key, "HOME": "/root"},
		CPULimit:    2, MemoryLimitBytes: 2 << 30,
	}
	if _, err := e.create(t.Context(), key, spec, options{}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	started := func(want string) {
		t.Helper()
		att, err := e.attach(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = att.Close() }()
		readUntil(t, att, want)
	}
	started("task-started")
	// reopen closes the session and starts it from its disks, and returns
	// the disk it kept while closed.
	reopen := func(what string) int64 {
		t.Helper()
		if err := e.pause(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		if err := e.stop(t.Context(), key, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		if info, err := e.inspect(t.Context(), key); err != nil || !info.Exited {
			t.Fatalf("the closed VM inspects as %+v, %v", info, err)
		}
		use, err := e.diskUse()
		if err != nil {
			t.Fatal(err)
		}
		if err = e.setCommand(key, []string{"/bin/sh", "-c", "echo conversation-reopened; exec sleep infinity"}); err != nil {
			t.Fatal(err)
		}
		begin := time.Now()
		if err = e.start(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		started("conversation-reopened")
		kept := use[runtime.ID(idPrefix+key)]
		t.Logf("%s: the closed session kept %d MiB of disk, and its new command ran %s after the start began", what, kept>>20, time.Since(begin).Round(10*time.Millisecond))
		return kept
	}
	fresh := reopen("a fresh session")
	build := `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends gcc libc6-dev make >/dev/null
mkdir -p /opt/app && cd /opt/app
printf '#include <stdio.h>\nint main(void) { puts("built in the session"); return 0; }\n' > hello.c
printf 'hello: hello.c\n\tgcc -O2 -o hello hello.c\n' > Makefile
make -s
sync`
	if out, code := vmRun(t, e, key, build); code != 0 {
		t.Fatalf("install packages and build: %d %s", code, out)
	}
	if grown := reopen("a session with gcc, libc6-dev, make and a build"); grown <= fresh {
		t.Errorf("the closed session kept %d bytes with packages installed and %d fresh", grown, fresh)
	}
	out, code := vmRun(t, e, key, "/opt/app/hello && gcc --version | head -n 1 && wc -l < /var/tmp/launches && wc -l < /var/tmp/setups")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 4 || lines[0] != "built in the session" || !strings.Contains(lines[1], "gcc") || lines[2] != "1" || lines[3] != "1" {
		t.Fatalf("after two starts from its disks the session has %d %q; want the built program, gcc, one launch and one setup run", code, out)
	}
}
