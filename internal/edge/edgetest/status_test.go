package edgetest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// Environment of the server process TestKilledServerStatusIsStale starts.
const (
	childDirEnv  = "AETHER_EDGETEST_SERVER_DIR"
	childEdgeEnv = "AETHER_EDGETEST_SERVER_EDGE"
)

// TestServerProcess is the server process TestKilledServerStatusIsStale
// kills: run without its environment, it does nothing.
func TestServerProcess(t *testing.T) {
	dir := os.Getenv(childDirEnv)
	if dir == "" {
		t.Skip("runs only as the server process of TestKilledServerStatusIsStale")
	}
	s := &serverNode{h: &harness{t: t, relayURL: os.Getenv(childEdgeEnv)}, dir: dir,
		policy: edgeproto.PolicyApprovedDevices, addr: "127.0.0.1:0"}
	s.start()
	select {}
}

// waitStatus waits until the server's status matches ok.
func (s *serverNode) waitStatus(what string, ok func(edgeagent.Status) error) edgeagent.Status {
	s.h.t.Helper()
	var st edgeagent.Status
	eventually(s.h.t, what, func() error {
		var err error
		if st, _, err = s.state().Status(); err != nil {
			return err
		}
		if err := ok(st); err != nil {
			return fmt.Errorf("%w; status %+v", err, st)
		}
		return nil
	})
	return st
}

func live(st edgeagent.Status) error {
	if !st.Connected {
		return errors.New("not connected")
	}
	return st.Stale(time.Now())
}

func down(st edgeagent.Status) error {
	if st.Connected {
		return errors.New("still connected")
	}
	return nil
}

// TestKilledServerStatusIsStale kills a claimed server's process, which
// writes no status as it dies. Its status still says connected, and Stale
// says the process is gone; the edge has dropped the registration, so a
// client is told the server is not connected.
func TestKilledServerStatusIsStale(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	dir := t.TempDir()
	hostKey, err := sshd.LoadOrCreateHostKey(filepath.Join(dir, "ssh", "host_ed25519_key"))
	if err != nil {
		t.Fatal(err)
	}
	a := &serverNode{h: h, id: edgeproto.ServerID(hostKey.PublicKey()), dir: dir}

	child := exec.Command(os.Args[0], "-test.run=^TestServerProcess$")
	child.Env = append(os.Environ(), childDirEnv+"="+dir, childEdgeEnv+"="+h.relayURL)
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	a.waitStatus("the server process connected", func(st edgeagent.Status) error {
		if st.PID != child.Process.Pid {
			return fmt.Errorf("pid %d, want %d", st.PID, child.Process.Pid)
		}
		return live(st)
	})
	al := h.login(alice)[0]
	h.claimServer(al, a)
	h.mustDial(al, a)

	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	st, _, err := a.state().Status()
	if err != nil || !st.Connected {
		t.Fatalf("status after the kill = %+v, %v; want the connected status it left", st, err)
	}
	if stale := st.Stale(time.Now()); stale == nil || !strings.Contains(stale.Error(), fmt.Sprintf("server process %d is not running", child.Process.Pid)) {
		t.Fatalf("Stale after the kill = %v", stale)
	}
	eventually(t, "the edge drops the killed server", func() error {
		if h.relay().Online(a.id) {
			return errors.New("still online")
		}
		return nil
	})
	_, err = h.dial(al, h.link(a))
	wantRefusal(t, "connect to the killed server", err, edgeproto.RefusalNotConnected)
}

// TestStatusFollowsTheConnection walks a claimed server's status through
// an edge restart and a cut network, and a client's connections with it:
// connected only while a client gets through.
func TestStatusFollowsTheConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	al := h.login(alice)[0]
	h.claimServer(al, a)
	first := a.waitStatus("connected", live)
	h.mustDial(al, a)

	h.stopEdge()
	st := a.waitStatus("disconnected by the drain", down)
	if !strings.Contains(st.Error, "edge is restarting (drain)") {
		t.Fatalf("status after the edge stopped: %+v", st)
	}
	if _, err := h.dial(al, h.link(a)); err == nil {
		t.Fatal("connected through a stopped edge")
	}
	h.startEdge()
	again := a.waitStatus("connected to the restarted edge", live)
	if !again.Since.After(first.Since) {
		t.Fatalf("connected since %s, the first connection's time", again.Since)
	}
	h.mustDial(al, a)

	h.proxy.cutServers()
	st = a.waitStatus("disconnected by the cut", down)
	if st.Error == "" {
		t.Fatalf("status after the cut names no cause: %+v", st)
	}
	_, err := h.dial(al, h.link(a))
	wantRefusal(t, "connect while the server is cut off", err, edgeproto.RefusalNotConnected)
	h.proxy.restoreServers()
	a.waitStatus("connected after the cut", live)
	eventually(t, "the edge relays the server again", func() error {
		if !h.relay().Online(a.id) {
			return errors.New("not online")
		}
		return nil
	})
	h.mustDial(al, a)
}

// TestUnclaimedServerIsNotReportedDisconnected connects a server nobody
// claimed. Its status says connected, and a signed-in client is told the
// edge knows no such server, never that it is not connected; a claim
// connection reaches it.
func TestUnclaimedServerIsNotReportedDisconnected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	a.waitStatus("connected, unclaimed", live)
	if h.relay().Online(a.id) {
		t.Fatal("an unclaimed server is listed online")
	}
	al := h.login(alice)[0]
	_, err := h.dial(al, h.link(a))
	wantRefusal(t, "connect to the unclaimed server", err, edgeproto.RefusalUnknownServer)
	h.claimServer(al, a)
	h.mustDial(al, a)
}
