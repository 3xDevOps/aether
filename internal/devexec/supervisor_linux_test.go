//go:build linux

package devexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Run must be tested in its own process: subreaping, terminal foreground groups
// and signal handlers must never change those of the test runner.
func TestSupervisorSubprocess(t *testing.T) {
	if os.Getenv("AETHER_DEVEXEC_SUBPROCESS") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(125)
	}
	switch args[0] {
	case "run":
		code, err := Run(args[1], args[2], args[3:])
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	case "input":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			os.Exit(124)
		}
		_, _ = fmt.Fprintf(os.Stdout, "received:%s", line)
		os.Exit(19)
	case "tree", "tree-exit", "zombie":
		signal.Ignore(syscall.SIGTERM)
		role := "escape"
		if args[0] == "zombie" {
			role = "escape-exit"
		}
		child := exec.Command(os.Args[0], "-test.run=^TestSupervisorSubprocess$", "--", role, args[1])
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Run(); err != nil {
			os.Exit(124)
		}
		if args[0] == "tree-exit" {
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			os.Exit(23)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "escape", "escape-exit":
		role := "hold"
		if args[0] == "escape-exit" {
			role = "exit"
		}
		child := exec.Command(os.Args[0], "-test.run=^TestSupervisorSubprocess$", "--", role, args[1])
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(124)
		}
		os.Exit(0)
	case "hold", "exit":
		signal.Ignore(syscall.SIGTERM)
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(124)
		}
		_, _ = fmt.Fprintln(os.Stdout, "descendant-ready")
		if args[0] == "exit" {
			os.Exit(23)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(125)
	}
}

type supervisorProcess struct {
	key     string
	process *os.Process
	request Request
	pty     *os.File
	lines   <-chan string
	exit    <-chan error
}

func startSupervisor(t *testing.T, argv ...string) *supervisorProcess {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	key := t.TempDir()
	claim := "test-attempt"
	args := append([]string{"-test.run=^TestSupervisorSubprocess$", "--", "run", key, claim}, argv...)
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "AETHER_DEVEXEC_SUBPROCESS=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait(); close(exit) }()
	request := Request{ExecID: "test-exec", ClaimToken: claim, Action: "start"}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		stop := request
		stop.Action = "stop"
		_, _ = Control(ctx, key, stop)
		select {
		case <-exit:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-exit
		}
		_ = os.RemoveAll(stateDir(key))
	})
	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(master)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return &supervisorProcess{key: key, process: cmd.Process, request: request, pty: master, lines: lines, exit: exit}
}

func (p *supervisorProcess) line(t *testing.T, want string) {
	t.Helper()
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				t.Fatalf("PTY ended before %q", want)
			}
			if strings.TrimSpace(line) == want {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("PTY did not produce %q", want)
		}
	}
}

func (p *supervisorProcess) exited(t *testing.T, want int) {
	t.Helper()
	select {
	case err := <-p.exit:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != want {
			t.Fatalf("supervisor exit = %v, want %d", err, want)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("supervisor did not exit within cleanup bound")
	}
}

func TestSupervisorClaimInputExitAndSingleUse(t *testing.T) {
	p := startSupervisor(t, os.Args[0], "-test.run=^TestSupervisorSubprocess$", "--", "input")
	wrong := p.request
	wrong.ClaimToken = "different-attempt"
	if _, err := Control(t.Context(), p.key, wrong); err == nil {
		t.Fatal("a different attempt claimed the waiting supervisor")
	}
	// An initial resize may arrive before the runtime claims the command.
	if err := p.process.Signal(syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	if _, err := Control(t.Context(), p.key, p.request); err != nil {
		t.Fatal(err)
	}
	if _, err := Control(t.Context(), p.key, p.request); err == nil {
		t.Fatal("start silently reused a live creation key")
	}
	if _, err := io.WriteString(p.pty, "hello\n"); err != nil {
		t.Fatal(err)
	}
	p.line(t, "received:hello")
	p.exited(t, 19)
	request := p.request
	request.Action = "status"
	state, err := Control(t.Context(), p.key, request)
	if err != nil || !state.Exited || state.Running || state.ExitCode == nil || *state.ExitCode != 19 {
		t.Fatalf("completed status = %+v, %v", state, err)
	}
	if _, err := Control(t.Context(), p.key, p.request); err == nil {
		t.Fatal("start silently reused a completed creation key")
	}
}

func TestSupervisorReapsEscapedDescendants(t *testing.T) {
	for _, mode := range []string{"tree", "tree-exit", "zombie"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "descendant.pid")
			p := startSupervisor(t, os.Args[0], "-test.run=^TestSupervisorSubprocess$", "--", mode, pidFile)
			if _, err := Control(t.Context(), p.key, p.request); err != nil {
				t.Fatal(err)
			}
			p.line(t, "descendant-ready")
			pidBytes, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(pidBytes))
			if err != nil || pid <= 0 {
				t.Fatalf("descendant identity = %q: %v", pidBytes, err)
			}
			request := p.request
			request.Action, request.GraceMillis = "stop", 20
			want := 137
			switch mode {
			case "tree":
				if err := unix.Kill(pid, 0); err != nil {
					t.Fatalf("descendant was not live before stop: %v", err)
				}
				if _, err := Control(t.Context(), p.key, request); err != nil {
					t.Fatal(err)
				}
			case "tree-exit":
				if _, err := io.WriteString(p.pty, "exit now\n"); err != nil {
					t.Fatal(err)
				}
				want = 23
			case "zombie":
				deadline := time.Now().Add(5 * time.Second)
				for !errors.Is(unix.Kill(pid, 0), unix.ESRCH) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
					t.Fatalf("live supervisor retained an adopted zombie: %v", err)
				}
				status := p.request
				status.Action = "status"
				state, err := Control(t.Context(), p.key, status)
				if err != nil || !state.Running || state.Exited {
					t.Fatalf("reaping a descendant stopped the root command: %+v, %v", state, err)
				}
				if _, err := Control(t.Context(), p.key, request); err != nil {
					t.Fatal(err)
				}
			}
			p.exited(t, want)
			if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
				t.Fatalf("owned escaped descendant survived/remained a zombie: %v", err)
			}
			state, err := Control(t.Context(), p.key, request)
			if err != nil || state.ExitCode == nil || *state.ExitCode != want {
				t.Fatalf("repeated stop lost the original exit status: %+v, %v", state, err)
			}
		})
	}
}

func TestSupervisorMissingCommandReportsFinalExit(t *testing.T) {
	p := startSupervisor(t, filepath.Join(t.TempDir(), "absent-command"))
	state, err := Control(t.Context(), p.key, p.request)
	if err == nil || state.ExitCode == nil || *state.ExitCode != 127 || !state.Exited {
		t.Fatalf("missing command = %+v, %v", state, err)
	}
	p.exited(t, 127)
	request := p.request
	request.Action = "status"
	state, err = Control(t.Context(), p.key, request)
	if err != nil || state.ExitCode == nil || *state.ExitCode != 127 {
		t.Fatalf("missing command lost its final exit record: %+v, %v", state, err)
	}
}

func TestControlCancellationUnblocksConnectedRequest(t *testing.T) {
	key := t.TempDir()
	if err := os.Mkdir(stateDir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir(key)) })
	listener, err := net.Listen("unix", filepath.Join(stateDir(key), "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request Request
		if json.NewDecoder(conn).Decode(&request) != nil {
			return
		}
		close(accepted)
		<-release
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := Control(ctx, key, Request{ExecID: "exec", ClaimToken: "claim", Action: "status"})
		result <- err
	}()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("control request did not reach the supervisor socket")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled control = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("connected control ignored cancellation until its startup deadline")
	}
}
