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
	case "run", "run-pipe":
		code, err := Run(args[1], args[2], args[0] == "run", args[3:])
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	case "pipe-input":
		if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			_ = tty.Close()
			os.Exit(121)
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			os.Exit(124)
		}
		_, _ = fmt.Fprintf(os.Stdout, "received:%s", line)
		_, _ = fmt.Fprintln(os.Stderr, "diagnostic")
		os.Exit(19)
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
	if unlockErr := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slave.Close() }()
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

func TestSupervisorPipeRunsWithoutTerminal(t *testing.T) {
	key := t.TempDir()
	claim := "test-attempt"
	cmd := exec.Command(os.Args[0], "-test.run=^TestSupervisorSubprocess$", "--", "run-pipe", key, claim,
		os.Args[0], "-test.run=^TestSupervisorSubprocess$", "--", "pipe-input")
	cmd.Env = append(os.Environ(), "AETHER_DEVEXEC_SUBPROCESS=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdin, pipeErr := cmd.StdinPipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if startErr := cmd.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir(key)) })
	request := Request{ExecID: "test-exec", ClaimToken: claim, Action: "start"}
	if _, controlErr := Control(t.Context(), key, request); controlErr != nil {
		_ = cmd.Process.Kill()
		t.Fatal(controlErr)
	}
	if _, writeErr := io.WriteString(stdin, "hello\n"); writeErr != nil {
		t.Fatal(writeErr)
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 19 {
		t.Fatalf("pipe supervisor exit = %v, stderr %q; want 19 (121 means the child had a terminal)", err, stderr.String())
	}
	if stdout.String() != "received:hello\n" || !strings.Contains(stderr.String(), "diagnostic") {
		t.Fatalf("pipe stdio = stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	request.Action = "status"
	state, err := Control(t.Context(), key, request)
	if err != nil || !state.Exited || state.ExitCode == nil || *state.ExitCode != 19 {
		t.Fatalf("pipe completed status = %+v, %v", state, err)
	}
}

func TestSupervisorPipePublishesDeviceCodeBeforeApproval(t *testing.T) {
	for _, action := range []string{"approve", "cancel"} {
		t.Run(action, func(t *testing.T) {
			key := t.TempDir()
			approval := filepath.Join(key, "approved")
			claim := "device-authorization"
			cmd := exec.Command(os.Args[0], "-test.run=^TestSupervisorSubprocess$", "--", "run-pipe", key, claim,
				"/bin/sh", "-c", `exec </dev/null
test ! -t 0 && test -z "$(cat)" || exit 124
printf '%s\n' '! First copy your one-time code: ABCD-1234' >&2
printf '%s\n' 'Open this URL to continue in your web browser: https://github.com/login/device'
while [ ! -f "$1" ]; do sleep 0.05; done`, "device-provider", approval)
			cmd.Env = append(os.Environ(), "AETHER_DEVEXEC_SUBPROCESS=1")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stdin.Close() })
			stdout, stdoutWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stdout.Close(); _ = stdoutWriter.Close() })
			stderr, stderrWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stderr.Close(); _ = stderrWriter.Close() })
			cmd.Stdout, cmd.Stderr = stdoutWriter, stderrWriter
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = stdoutWriter.Close()
			_ = stderrWriter.Close()
			exit := make(chan error, 1)
			go func() { exit <- cmd.Wait(); close(exit) }()
			request := Request{ExecID: "device-exec", ClaimToken: claim, Action: "start"}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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
			// Keep the attachment's stdin open, as Docker does; only the
			// provider's explicit redirection should supply EOF.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			state, err := Control(ctx, key, request)
			if err != nil || !state.Running {
				t.Fatalf("claim = %+v, %v", state, err)
			}
			for _, stream := range []struct {
				reader io.Reader
				want   string
			}{
				{stderr, "! First copy your one-time code: ABCD-1234"},
				{stdout, "Open this URL to continue in your web browser: https://github.com/login/device"},
			} {
				line := make(chan string, 1)
				go func() {
					scanner := bufio.NewScanner(stream.reader)
					if scanner.Scan() {
						line <- scanner.Text()
					}
					close(line)
				}()
				select {
				case got := <-line:
					if got != stream.want {
						t.Fatalf("live pipe output = %q, want %q", got, stream.want)
					}
				case <-ctx.Done():
					t.Fatal("device output waited for provider approval or process exit")
				}
			}
			request.Action = "status"
			state, err = Control(ctx, key, request)
			if err != nil || !state.Running || state.Exited {
				t.Fatalf("provider stopped before approval: %+v, %v", state, err)
			}
			wantCode := 0
			if action == "approve" {
				if err = os.WriteFile(approval, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				request.Action, request.GraceMillis = "stop", 100
				if _, err = Control(ctx, key, request); err != nil {
					t.Fatal(err)
				}
				wantCode = 143
			}
			select {
			case waitErr := <-exit:
				var exited *exec.ExitError
				if (wantCode == 0 && waitErr != nil) || (wantCode != 0 && (!errors.As(waitErr, &exited) || exited.ExitCode() != wantCode)) {
					t.Fatalf("provider %s exit = %v, want %d", action, waitErr, wantCode)
				}
			case <-ctx.Done():
				t.Fatalf("provider did not finish after %s", action)
			}
			request.Action = "status"
			state, err = Control(ctx, key, request)
			if err != nil || !state.Exited || state.ExitCode == nil || *state.ExitCode != wantCode {
				t.Fatalf("final provider status = %+v, %v", state, err)
			}
		})
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
			pidBytes, readPIDErr := os.ReadFile(pidFile)
			if readPIDErr != nil {
				t.Fatal(readPIDErr)
			}
			pid, parsePIDErr := strconv.Atoi(string(pidBytes))
			if parsePIDErr != nil || pid <= 0 {
				t.Fatalf("descendant identity = %q: %v", pidBytes, parsePIDErr)
			}
			request := p.request
			request.Action, request.GraceMillis = "stop", 20
			want := 137
			switch mode {
			case "tree":
				if killErr := unix.Kill(pid, 0); killErr != nil {
					t.Fatalf("descendant was not live before stop: %v", killErr)
				}
				if _, controlErr := Control(t.Context(), p.key, request); controlErr != nil {
					t.Fatal(controlErr)
				}
			case "tree-exit":
				if _, writeErr := io.WriteString(p.pty, "exit now\n"); writeErr != nil {
					t.Fatal(writeErr)
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
				state, statusErr := Control(t.Context(), p.key, status)
				if statusErr != nil || !state.Running || state.Exited {
					t.Fatalf("reaping a descendant stopped the root command: %+v, %v", state, statusErr)
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
	defer func() { _ = listener.Close() }()
	accepted := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
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
