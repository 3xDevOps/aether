package scheduler

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// testdata/listening-ports.txt is listeningScript's output in a run container
// where one python process held 0.0.0.0:8080, [::]:8080, the IPv4-mapped
// [::ffff:127.0.0.1]:7000 and 127.0.0.1:9000 and shared them with a forked
// child, a second python served 0.0.0.0:8000 and node served [::1]:5173.
func TestParseListeningPortsFromACapturedRun(t *testing.T) {
	captured, err := os.ReadFile(filepath.Join("testdata", "listening-ports.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.DevPort{
		{Port: 5173, Address: "::1", Process: "node"},
		{Port: 7000, Address: "127.0.0.1", Process: "python3.12"},
		{Port: 8000, Address: "0.0.0.0", Process: "python3.12"},
		{Port: 8080, Address: "0.0.0.0", Process: "python3.12"},
		{Port: 9000, Address: "127.0.0.1", Process: "python3.12"},
	}
	if got := parseListeningPorts(string(captured)); !slices.Equal(got, want) {
		t.Fatalf("ports = %+v, want %+v", got, want)
	}
}

// TestListeningScriptFindsThisProcess runs the real script against this
// machine's /proc, where the test binary itself is listening.
func TestListeningScriptFindsThisProcess(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", listeningScript).Output()
	if err != nil {
		t.Fatalf("listening script: %v", err)
	}
	want := protocol.DevPort{Port: listener.Addr().(*net.TCPAddr).Port, Address: "127.0.0.1", Process: filepath.Base(binary)}
	if got := parseListeningPorts(string(out)); !slices.Contains(got, want) {
		t.Fatalf("ports = %+v, want %+v among them; script output:\n%s", got, want, out)
	}
}

func TestDevelopmentPortList(t *testing.T) {
	e, run := developmentFixture(t)
	captured, err := os.ReadFile(filepath.Join("testdata", "listening-ports.txt"))
	if err != nil {
		t.Fatal(err)
	}
	answer := func(code int, stdout, stderr string) {
		e.rt.mu.Lock()
		defer e.rt.mu.Unlock()
		e.rt.execStderr = stderr
		e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
			if !slices.Equal(argv, []string{"/bin/sh", "-c", listeningScript}) {
				return 0, "", nil
			}
			return code, stdout, nil
		}
	}
	list := func() (string, error) {
		result, err := e.sched.CallDevelopment(t.Context(), run.ID, control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}, protocol.MethodDevPortList, json.RawMessage(`{"run_id":"`+string(run.ID)+`"}`), func() error { return nil })
		if err != nil {
			return "", err
		}
		encoded, err := json.Marshal(result)
		return string(encoded), err
	}

	answer(0, string(captured), "")
	const listed = `{"ports":[{"port":5173,"address":"::1","process":"node"},{"port":7000,"address":"127.0.0.1","process":"python3.12"},{"port":8000,"address":"0.0.0.0","process":"python3.12"},{"port":8080,"address":"0.0.0.0","process":"python3.12"},{"port":9000,"address":"127.0.0.1","process":"python3.12"}]}`
	if got, err := list(); err != nil || got != listed {
		t.Fatalf("ports = %s, %v", got, err)
	}

	answer(0, "", "")
	if got, err := list(); err != nil || got != `{"ports":[]}` {
		t.Fatalf("with nothing listening = %s, %v", got, err)
	}

	answer(1, "", "grep: /proc/net/tcp: Permission denied\n")
	if _, err := list(); err == nil || err.Error() != "read the run's listening ports: exit 1: grep: /proc/net/tcp: Permission denied" {
		t.Fatalf("unreadable socket table: %v", err)
	}

	c := e.rt.byName(string(run.ID))
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if _, err := list(); !errors.Is(err, ErrNoLiveEnvironment) {
		t.Fatalf("finished run: %v", err)
	}
}
