package scheduler

import (
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/coordhooks"
	"github.com/3xDevOps/cloudfield/internal/coordtransport"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// The server names the new command only in a container whose mounts
// include its path: one an older server created gets the old name, and so
// do a container it cannot inspect and a VM session, whose mounts are its
// spec rather than what its guest bound.
func TestContainerCLIFollowsTheContainersMounts(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime()
	created := func(paths ...string) runtime.ID {
		var mounts []runtime.Mount
		for _, p := range paths {
			mounts = append(mounts, runtime.Mount{HostPath: "/staged/aether-server", ContainerPath: p, ReadOnly: true})
		}
		id, err := rt.Create(t.Context(), runtime.Spec{Name: string(rune('a' + len(paths))), Image: "img", Command: []string{"agent"}, Mounts: mounts})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	both, old := created(coordtransport.CLIPath, coordtransport.CloudfieldCLIPath), created(coordtransport.CLIPath)
	s := &Scheduler{cfg: Config{Runtime: rt}}
	for cid, want := range map[runtime.ID]string{both: coordhooks.NewCLI, old: coordhooks.OldCLI, "": coordhooks.OldCLI, "gone": coordhooks.OldCLI} {
		if got := s.containerCLI(t.Context(), cid); got != want {
			t.Errorf("containerCLI(%q) = %s, want %s", cid, got, want)
		}
	}
	if s.createdCLI() != coordhooks.NewCLI {
		t.Error("a container this server creates is not told the new name")
	}
	vm := &Scheduler{cfg: Config{Runtime: vmFakeRuntime{rt}}}
	if got := vm.containerCLI(t.Context(), both); got != coordhooks.OldCLI || vm.createdCLI() != coordhooks.OldCLI {
		t.Errorf("a VM session is told %s", got)
	}
}

// A wake prompt names the command the session's container has, now and
// after it turns out to be a container an older server created. The
// turn-end report reminder keeps its lead under either name, because
// released dashboards recognise it by that.
func TestWakePromptNamesTheContainersCommand(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	withCoordination(t, e)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	inbox := protocol.CoordInboxContext(1)
	reminder := coordhooks.ReportReminder(coordhooks.Guide{Role: coordhooks.RoleInteractive, Wake: true})
	wake := func(prompt, want string) {
		t.Helper()
		if err := e.sched.Wake(t.Context(), run.ID, prompt); err != nil {
			t.Fatalf("Wake: %v", err)
		}
		waitItems(t, e.sched, run.ID, "the woken turn's end", func(items []acphost.Item) bool {
			asked := slices.IndexFunc(items, func(it acphost.Item) bool {
				return it.Kind == acphost.KindMessage && it.Message.Role == "user" && it.Message.Text == want
			})
			return asked >= 0 && slices.ContainsFunc(items[asked:], func(it acphost.Item) bool { return it.Kind == acphost.KindTurnEnd })
		})
	}
	wake(inbox, strings.ReplaceAll(inbox, "aether-internal", "cloudfield-internal"))
	wake(reminder, reminder)

	c := e.rt.byName(string(run.ID))
	c.mu.Lock()
	c.spec.Mounts = slices.DeleteFunc(slices.Clone(c.spec.Mounts), func(m runtime.Mount) bool { return m.ContainerPath == coordtransport.CloudfieldCLIPath })
	c.mu.Unlock()
	wake(inbox+" ", inbox+" ")
}
