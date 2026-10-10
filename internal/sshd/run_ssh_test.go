package sshd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/devexec"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// runSSHTestService stands in for the run's container: commands run as
// local processes in dir, and the two helper entry points run in process.
// Authorization, the caps and the SSH protocol are the real server's.
type runSSHTestService struct {
	dir string

	mu      sync.Mutex
	refusal error
	started []scheduler.RunExec
	// stopped counts stops that landed; stopErr refuses them, as a paused
	// container does, and refusedStops counts those.
	stopped      int
	stopErr      error
	refusedStops int
}

// pause freezes the run as the scheduler reports it: new processes are
// refused by name and nothing in the container can be stopped.
func (f *runSSHTestService) pause(e *testEnv, paused bool) {
	e.runs.mu.Lock()
	e.runs.paused = map[domain.RunID]bool{e.run.ID: paused}
	e.runs.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusal, f.stopErr = nil, nil
	if paused {
		f.refusal, f.stopErr = scheduler.ErrRunPaused, errors.New("container is paused")
	}
}

func installRunSSHTestService(t *testing.T, e *testEnv) *runSSHTestService {
	t.Helper()
	installDevelopmentTestService(e)
	svc := &runSSHTestService{dir: t.TempDir()}
	e.srv.cfg.Services.RunSSH = svc
	return svc
}

func (f *runSSHTestService) refuse(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusal = err
}

func (f *runSSHTestService) starts() []scheduler.RunExec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scheduler.RunExec(nil), f.started...)
}

func (f *runSSHTestService) stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func (f *runSSHTestService) CheckRunExec(_ context.Context, _ domain.RunID, p control.Principal, authorize func() error) error {
	if p.Kind != control.PrincipalMember || p.RunID != "" {
		panic("ssh transport spoofed principal")
	}
	if err := authorize(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refusal
}

func (f *runSSHTestService) StartRunExec(ctx context.Context, run domain.RunID, p control.Principal, spec scheduler.RunExec, authorize func() error) (runtime.ManagedExec, error) {
	if err := f.CheckRunExec(ctx, run, p, authorize); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.started = append(f.started, spec)
	f.mu.Unlock()
	if len(spec.Argv) == 3 && spec.Argv[1] == devexec.Command {
		return f.helper(spec.Argv[2])
	}
	return f.process(spec)
}

// localExec is a runtime.ManagedExec over a local process or goroutine.
type localExec struct {
	svc            *runSSHTestService
	stdin          io.WriteCloser
	stdout, stderr io.Reader
	kill           func()
	done           chan struct{}
	code           int
	resized        chan [2]uint
}

func (e *localExec) Identity() runtime.ExecIdentity { return runtime.ExecIdentity{ExecID: "local"} }
func (e *localExec) Attachment() runtime.Attachment { return localAttachment{e} }
func (e *localExec) Detach() error                  { return nil }

func (e *localExec) Status(context.Context) (runtime.ExecState, error) {
	select {
	case <-e.done:
		return runtime.ExecState{Exited: true, ExitCode: &e.code}, nil
	default:
		return runtime.ExecState{Running: true, Attached: true}, nil
	}
}

func (e *localExec) Wait(ctx context.Context) (runtime.ExitStatus, error) {
	select {
	case <-e.done:
		return runtime.ExitStatus{Code: e.code}, nil
	case <-ctx.Done():
		return runtime.ExitStatus{}, ctx.Err()
	}
}

func (e *localExec) Stop(ctx context.Context, _ time.Duration) (runtime.ExitStatus, error) {
	e.svc.mu.Lock()
	if err := e.svc.stopErr; err != nil {
		e.svc.refusedStops++
		e.svc.mu.Unlock()
		return runtime.ExitStatus{}, err
	}
	e.svc.stopped++
	e.svc.mu.Unlock()
	e.kill()
	return e.Wait(ctx)
}

func (e *localExec) Resize(_ context.Context, cols, rows uint) error {
	e.resized <- [2]uint{cols, rows}
	return nil
}

type localAttachment struct{ e *localExec }

func (a localAttachment) Stdin() io.WriteCloser { return a.e.stdin }
func (a localAttachment) Stdout() io.Reader     { return a.e.stdout }
func (a localAttachment) Stderr() io.Reader     { return a.e.stderr }
func (a localAttachment) Close() error          { a.e.kill(); return nil }
func (a localAttachment) Resize(ctx context.Context, cols, rows uint) error {
	return a.e.Resize(ctx, cols, rows)
}

func (f *runSSHTestService) process(spec scheduler.RunExec) (runtime.ManagedExec, error) {
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = f.dir
	cmd.Env = append(os.Environ(), spec.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	if spec.TTY {
		cmd.Stderr = outW
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	_ = outW.Close()
	_ = errW.Close()
	e := &localExec{svc: f, stdin: stdin, stdout: outR, stderr: errR, done: make(chan struct{}), resized: make(chan [2]uint, 8)}
	e.kill = func() { _ = cmd.Process.Kill() }
	go func() {
		_ = cmd.Wait()
		e.code = cmd.ProcessState.ExitCode()
		close(e.done)
	}()
	return e, nil
}

// helper runs a staged-binary entry point in process. Its stdio is a pair of
// OS pipes because both ends of an SSH handshake write before they read.
func (f *runSSHTestService) helper(name string) (runtime.ManagedExec, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	e := &localExec{svc: f, stdin: inW, stdout: outR, stderr: strings.NewReader(""), done: make(chan struct{}), resized: make(chan [2]uint, 8)}
	closeAll := func() {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
	}
	e.kill = closeAll
	stream := struct {
		io.Reader
		io.Writer
		io.Closer
	}{inR, outW, closerFunc(func() error { closeAll(); return nil })}
	go func() {
		defer close(e.done)
		defer closeAll()
		if name == devexec.SFTP {
			_ = devexec.ServeSFTP(stream)
			return
		}
		_ = devexec.ServeForward(devexec.Stream{Reader: stream, Writer: stream, Closer: stream})
	}()
	return e, nil
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// dialRunSSH opens the aether-run-ssh subsystem and, when admitted, the SSH
// connection nested in it, checking the host key the ack names.
func dialRunSSH(t *testing.T, client *ssh.Client, run string) (*ssh.Client, protocol.RunSSHResponse) {
	t.Helper()
	pipe := openSubsystem(t, client, protocol.SubsystemRunSSH, nil)
	header, err := json.Marshal(protocol.RunSSHRequest{RunID: run})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pipe.Write(append(header, '\n')); err != nil {
		t.Fatalf("write header: %v", err)
	}
	r := bufio.NewReader(pipe)
	line, err := protocol.ReadLine(r)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	var ack protocol.RunSSHResponse
	if err = json.Unmarshal(line, &ack); err != nil {
		t.Fatalf("decode ack %q: %v", line, err)
	}
	if !ack.OK {
		return nil, ack
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(ack.HostKey))
	if err != nil {
		t.Fatalf("ack host key %q: %v", ack.HostKey, err)
	}
	// The client transport closes its stream while it may still be writing
	// to it, which closing the channel allows and the pipe's half-close
	// does not.
	closer := closerFunc(pipe.sess.Close)
	conn, chans, reqs, err := ssh.NewClientConn(devexec.Stream{Reader: r, Writer: pipe, Closer: closer}, "aether",
		&ssh.ClientConfig{User: "anyone", HostKeyCallback: ssh.FixedHostKey(hostKey)})
	if err != nil {
		t.Fatalf("nested ssh handshake: %v", err)
	}
	nested := ssh.NewClient(conn, chans, reqs)
	t.Cleanup(func() { _ = nested.Close() })
	return nested, ack
}

func mustDialRunSSH(t *testing.T, e *testEnv, signer ssh.Signer) *ssh.Client {
	t.Helper()
	nested, ack := dialRunSSH(t, e.dialWithTest(t, signer), string(e.run.ID))
	if nested == nil {
		t.Fatalf("ssh into the run refused: %d %s", ack.Code, ack.Error)
	}
	return nested
}

func runOverSSH(t *testing.T, client *ssh.Client, command, stdin string) (stdout, stderr string, status int) {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer func() { _ = sess.Close() }()
	var out, errOut bytes.Buffer
	sess.Stdin, sess.Stdout, sess.Stderr = strings.NewReader(stdin), &out, &errOut
	err = sess.Run(command)
	var exit *ssh.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		status = exit.ExitStatus()
	default:
		t.Fatalf("run %q: %v", command, err)
	}
	return out.String(), errOut.String(), status
}

func TestRunSSHExecCarriesStreamsAndExitStatus(t *testing.T) {
	e := newTestEnv(t, nil)
	svc := installRunSSHTestService(t, e)
	client := mustDialRunSSH(t, e, e.signer)

	stdout, stderr, status := runOverSSH(t, client, `echo out; echo err >&2; exit 7`, "")
	if stdout != "out\n" || stderr != "err\n" || status != 7 {
		t.Fatalf("exec = stdout %q stderr %q status %d", stdout, stderr, status)
	}
	// End of input reaches the command, as scp, rsync and an editor's
	// bootstrap script all rely on, and one connection carries more than
	// one session.
	if stdout, _, status = runOverSSH(t, client, `cat; pwd`, "piped\n"); status != 0 || stdout != "piped\n"+svc.dir+"\n" {
		t.Fatalf("stdin and working directory = %q status %d, want the run's checkout %s", stdout, status, svc.dir)
	}
	if got := len(svc.starts()); got != 2 {
		t.Fatalf("executions started = %d, want one per session", got)
	}
}

func TestRunSSHSessionRequests(t *testing.T) {
	e := newTestEnv(t, nil)
	svc := installRunSSHTestService(t, e)
	client := mustDialRunSSH(t, e, e.signer)

	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err = sess.Setenv("LC_ALL", "C.UTF-8"); err != nil {
		t.Fatalf("locale variable refused: %v", err)
	}
	if err = sess.Setenv("AETHER_RUN_ID", "run-other"); err == nil {
		t.Fatal("an arbitrary environment variable was accepted")
	}
	if ok, rerr := sess.SendRequest("auth-agent-req@openssh.com", true, nil); rerr != nil || ok {
		t.Fatalf("agent forwarding = %v %v, want refused", ok, rerr)
	}
	if err = sess.RequestPty("xterm-256color", 30, 100, ssh.TerminalModes{}); err != nil {
		t.Fatalf("pty-req: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	sess.Stdout = &out
	if err = sess.Shell(); err != nil {
		t.Fatalf("shell: %v", err)
	}
	if err = sess.WindowChange(40, 120); err != nil {
		t.Fatalf("window-change: %v", err)
	}
	if _, err = io.WriteString(stdin, "echo $TERM $LC_ALL\nexit 3\n"); err != nil {
		t.Fatal(err)
	}
	var exit *ssh.ExitError
	if err = sess.Wait(); !errors.As(err, &exit) || exit.ExitStatus() != 3 {
		t.Fatalf("shell exit = %v, want status 3", err)
	}
	if !strings.Contains(out.String(), "xterm-256color C.UTF-8") {
		t.Fatalf("shell output = %q, want the terminal type and locale", out.String())
	}
	started := svc.starts()
	if len(started) != 1 || !started[0].TTY || started[0].Cols != 100 || started[0].Rows != 30 {
		t.Fatalf("shell execution = %+v, want a 100x30 terminal", started)
	}

	other, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if err = other.RequestSubsystem("shell-escape"); err == nil {
		t.Fatal("an unknown subsystem was accepted")
	}
}

func TestRunSSHRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, e *testEnv, svc *runSSHTestService) (signer ssh.Signer, run string)
		code    int
		message string
	}{
		{
			name: "another member's account is not shared",
			arrange: func(t *testing.T, e *testEnv, _ *runSSHTestService) (ssh.Signer, string) {
				signer, _ := addMember(t, e, "Bo", domain.RoleCollaborator, false)
				return signer, string(e.run.ID)
			},
			code: protocol.CodeDenied, message: "has not shared their account with you",
		},
		{
			name: "a protected run",
			arrange: func(t *testing.T, e *testEnv, _ *runSSHTestService) (ssh.Signer, string) {
				signer, bo := addMember(t, e, "Bo", domain.RoleCollaborator, false)
				if err := e.store.ShareAccount(t.Context(), e.member.ID, bo.ID); err != nil {
					t.Fatal(err)
				}
				e.run.Protected = true
				if err := e.store.UpdateRun(t.Context(), e.run); err != nil {
					t.Fatal(err)
				}
				return signer, string(e.run.ID)
			},
			code: protocol.CodeDenied, message: "permission denied",
		},
		{
			name: "a viewer",
			arrange: func(t *testing.T, e *testEnv, _ *runSSHTestService) (ssh.Signer, string) {
				signer, bo := addMember(t, e, "Bo", domain.RoleViewer, false)
				if err := e.store.ShareAccount(t.Context(), e.member.ID, bo.ID); err != nil {
					t.Fatal(err)
				}
				return signer, string(e.run.ID)
			},
			code: protocol.CodeDenied, message: "permission denied",
		},
		{
			name: "a pending member",
			arrange: func(t *testing.T, e *testEnv, _ *runSSHTestService) (ssh.Signer, string) {
				signer, _ := addMember(t, e, "Bo", domain.RoleCollaborator, true)
				return signer, string(e.run.ID)
			},
			code: protocol.CodeDenied, message: "membership pending admin approval",
		},
		{
			name: "an unknown run",
			arrange: func(_ *testing.T, e *testEnv, _ *runSSHTestService) (ssh.Signer, string) {
				return e.signer, "run-nope"
			},
			code: protocol.CodeNotFound, message: "run not found",
		},
		{
			name: "a paused run",
			arrange: func(_ *testing.T, e *testEnv, svc *runSSHTestService) (ssh.Signer, string) {
				svc.refuse(scheduler.ErrRunPaused)
				return e.signer, string(e.run.ID)
			},
			code: protocol.CodeInvalidState, message: "the run is paused",
		},
		{
			name: "a finished run",
			arrange: func(_ *testing.T, e *testEnv, svc *runSSHTestService) (ssh.Signer, string) {
				svc.refuse(fmt.Errorf("%w: the run is completed and its container is gone", scheduler.ErrNoLiveEnvironment))
				return e.signer, string(e.run.ID)
			},
			code: protocol.CodeInvalidState, message: "the run is completed and its container is gone",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			svc := installRunSSHTestService(t, e)
			signer, run := tc.arrange(t, e, svc)
			nested, ack := dialRunSSH(t, e.dialWithTest(t, signer), run)
			if nested != nil {
				t.Fatal("ssh into the run was admitted")
			}
			if ack.Code != tc.code || !strings.Contains(ack.Error, tc.message) {
				t.Fatalf("refusal = %d %q, want code %d naming %q", ack.Code, ack.Error, tc.code, tc.message)
			}
			if started := svc.starts(); len(started) != 0 {
				t.Fatalf("a refused connection started %+v", started)
			}
		})
	}
}

func TestRunSSHUnavailableWithoutService(t *testing.T) {
	e := newTestEnv(t, nil)
	if _, ack := dialRunSSH(t, e.dial(t), string(e.run.ID)); ack.Code != protocol.CodeUnavailable {
		t.Fatalf("refusal = %d %q, want unavailable", ack.Code, ack.Error)
	}
}

func TestRunSSHEndsWhenAuthorizationOrTheRunGoes(t *testing.T) {
	for name, revoke := range map[string]func(t *testing.T, e *testEnv, svc *runSSHTestService, bo *domain.Member){
		"steer withdrawn": func(t *testing.T, e *testEnv, _ *runSSHTestService, _ *domain.Member) {
			e.run.Protected = true
			if err := e.store.UpdateRun(t.Context(), e.run); err != nil {
				t.Fatal(err)
			}
		},
		"account share withdrawn": func(t *testing.T, e *testEnv, _ *runSSHTestService, bo *domain.Member) {
			if err := e.store.RevokeAccountShare(t.Context(), e.member.ID, bo.ID); err != nil {
				t.Fatal(err)
			}
		},
		"run finished": func(_ *testing.T, _ *testEnv, svc *runSSHTestService, _ *domain.Member) {
			svc.refuse(fmt.Errorf("%w: the run is completed and its container is gone", scheduler.ErrNoLiveEnvironment))
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t, func(c *Config) { c.revalidateInterval = 20 * time.Millisecond })
			svc := installRunSSHTestService(t, e)
			signer, bo := addMember(t, e, "Bo", domain.RoleCollaborator, false)
			if err := e.store.ShareAccount(t.Context(), e.member.ID, bo.ID); err != nil {
				t.Fatal(err)
			}
			client := mustDialRunSSH(t, e, signer)
			sess, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			if err = sess.Start("sleep 300"); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the command to start", func() bool { return len(svc.starts()) == 1 })
			closed := make(chan error, 1)
			go func() { closed <- client.Wait() }()
			revoke(t, e, svc, bo)
			select {
			case <-closed:
			case <-time.After(10 * time.Second):
				t.Fatal("the connection stayed open")
			}
			eventually(t, "the session's process to be stopped", func() bool { return svc.stops() == 1 })
		})
	}
}

func TestRunSSHSurvivesACheckThatCannotComplete(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.revalidateInterval = 10 * time.Millisecond })
	svc := installRunSSHTestService(t, e)
	client := mustDialRunSSH(t, e, e.signer)
	svc.refuse(errors.New("run lifecycle changed; retry"))
	time.Sleep(100 * time.Millisecond)
	svc.refuse(nil)
	if stdout, _, status := runOverSSH(t, client, "echo still here", ""); status != 0 || stdout != "still here\n" {
		t.Fatalf("after a transient check failure: %q status %d", stdout, status)
	}
}

func TestRunSSHPauseKeepsTheConnectionAndStopsItsProcessesOnResume(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.revalidateInterval = 10 * time.Millisecond })
	svc := installRunSSHTestService(t, e)
	client := mustDialRunSSH(t, e, e.signer)
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err = sess.Start("sleep 300"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the command to start", func() bool { return len(svc.starts()) == 1 })

	svc.pause(e, true)
	time.Sleep(100 * time.Millisecond)
	if _, stderr, status := runOverSSH(t, client, "echo unreachable", ""); status != 255 || !strings.Contains(stderr, "the run is paused") {
		t.Fatalf("new session on the paused run = status %d stderr %q, want it refused on the live connection", status, stderr)
	}

	// The member leaves while the container is frozen: the stop cannot
	// land until the run resumes, and must land then.
	_ = client.Close()
	eventually(t, "the refused stop", func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.refusedStops == 1
	})
	time.Sleep(100 * time.Millisecond)
	svc.mu.Lock()
	refused, landed := svc.refusedStops, svc.stopped
	svc.mu.Unlock()
	if refused != 1 || landed != 0 {
		t.Fatalf("while paused: %d refused and %d landed stops, want one attempt and no retries", refused, landed)
	}
	svc.pause(e, false)
	eventually(t, "the session's process to be stopped after the resume", func() bool { return svc.stops() == 1 })
}

func TestRunSSHDisconnectStopsItsProcesses(t *testing.T) {
	e := newTestEnv(t, nil)
	svc := installRunSSHTestService(t, e)
	client := mustDialRunSSH(t, e, e.signer)
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err = sess.Start("sleep 300"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the command to start", func() bool { return len(svc.starts()) == 1 })
	_ = client.Close()
	eventually(t, "the session's process to be stopped", func() bool { return svc.stops() == 1 })
}

func TestRunSSHStartRefusalReachesTheClient(t *testing.T) {
	e := newTestEnv(t, nil)
	svc := installRunSSHTestService(t, e)
	client := mustDialRunSSH(t, e, e.signer)
	svc.refuse(scheduler.ErrRunPaused)
	_, stderr, status := runOverSSH(t, client, "echo unreachable", "")
	if status != 255 || !strings.Contains(stderr, "aether: scheduler: the run has no live environment: the run is paused") {
		t.Fatalf("start refusal = status %d stderr %q", status, stderr)
	}
}

func listenLoopback(t *testing.T, reply string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				request, _ := io.ReadAll(conn)
				_, _ = io.WriteString(conn, reply+string(request))
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestRunSSHForwardReachesOnlyTheRunsLoopback(t *testing.T) {
	e := newTestEnv(t, nil)
	svc := installRunSSHTestService(t, e)
	client := mustDialRunSSH(t, e, e.signer)
	port := listenLoopback(t, "echo:")

	for _, host := range []string{"localhost", "127.0.0.1"} {
		conn, err := client.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			t.Fatalf("forward to %s: %v", host, err)
		}
		if _, err = io.WriteString(conn, "ping"); err != nil {
			t.Fatal(err)
		}
		// The half-close reaches the listener, and its reply still
		// comes back.
		if err = conn.(ssh.Channel).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(conn)
		if err != nil || string(got) != "echo:ping" {
			t.Fatalf("forward to %s = %q %v", host, got, err)
		}
		_ = conn.Close()
	}
	relays := 0
	for _, spec := range svc.starts() {
		if len(spec.Argv) == 3 && spec.Argv[2] == devexec.Forward {
			relays++
		}
	}
	if relays != 1 {
		t.Fatalf("relays started = %d, want one shared by the connection's forwards", relays)
	}

	for _, host := range []string{"example.com", "10.0.0.1", "169.254.169.254", "0.0.0.0", "terminal", "run:" + string(e.run.ID)} {
		_, err := client.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		var refused *ssh.OpenChannelError
		if !errors.As(err, &refused) || refused.Reason != ssh.Prohibited || !strings.Contains(refused.Message, "only the run's own loopback") {
			t.Fatalf("forward to %s = %v, want it prohibited", host, err)
		}
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unused := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	_, err = client.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(unused)))
	var refused *ssh.OpenChannelError
	if !errors.As(err, &refused) || refused.Reason != ssh.ConnectionFailed || !strings.Contains(refused.Message, "connection refused") {
		t.Fatalf("forward to a closed port = %v, want the dial error", err)
	}

	if _, err = client.Listen("tcp", "127.0.0.1:0"); err == nil {
		t.Fatal("reverse forwarding was accepted")
	}
}

func TestRunSSHBoundsSessionsForwardsAndConnections(t *testing.T) {
	e := newTestEnv(t, nil)
	installRunSSHTestService(t, e)
	client := mustDialRunSSH(t, e, e.signer)

	for i := range maxRunSSHSessions {
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("session %d: %v", i+1, err)
		}
		defer func() { _ = sess.Close() }()
	}
	_, err := client.NewSession()
	var refused *ssh.OpenChannelError
	if !errors.As(err, &refused) || refused.Reason != ssh.ResourceShortage {
		t.Fatalf("session past the cap = %v, want a resource shortage", err)
	}

	port := listenLoopback(t, "")
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for i := range maxRunSSHForwards {
		conn, derr := client.Dial("tcp", addr)
		if derr != nil {
			t.Fatalf("forward %d: %v", i+1, derr)
		}
		defer func() { _ = conn.Close() }()
	}
	if _, err = client.Dial("tcp", addr); !errors.As(err, &refused) || refused.Reason != ssh.ResourceShortage {
		t.Fatalf("forward past the cap = %v, want a resource shortage", err)
	}

	for i := 1; i < maxRunSSHConns; i++ {
		mustDialRunSSH(t, e, e.signer)
	}
	if nested, ack := dialRunSSH(t, e.dial(t), string(e.run.ID)); nested != nil || ack.Code != protocol.CodeConflict ||
		!strings.Contains(ack.Error, "already has 16 SSH connections open") {
		t.Fatalf("connection past the cap = %d %q", ack.Code, ack.Error)
	}
}

func TestRunSSHTimelineNamesWhoConnected(t *testing.T) {
	e := newTestEnv(t, nil)
	installRunSSHTestService(t, e)
	sub, err := e.bus.Subscribe(t.Context(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()

	first := mustDialRunSSH(t, e, e.signer)
	second := mustDialRunSSH(t, e, e.signer)
	select {
	case ev := <-sub.Events():
		payload, ok := ev.Payload.(events.TimelinePayload)
		if !ok || ev.ActorID != e.member.ID || ev.RunID != e.run.ID || payload.Kind != events.TimelineNote || payload.Message != "connected over SSH" {
			t.Fatalf("timeline entry = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no timeline entry for the connection")
	}
	select {
	case ev := <-sub.Events():
		t.Fatalf("a second concurrent connection was recorded again: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	_ = first.Close()
	_ = second.Close()
	eventually(t, "the connections to be released", func() bool {
		e.srv.runSSHMu.Lock()
		defer e.srv.runSSHMu.Unlock()
		return len(e.srv.runSSHConns) == 0
	})
	mustDialRunSSH(t, e, e.signer)
	select {
	case <-sub.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("a later connection was not recorded")
	}
}

func TestRunSSHSFTP(t *testing.T) {
	e := newTestEnv(t, nil)
	svc := installRunSSHTestService(t, e)
	client, err := sftp.NewClient(mustDialRunSSH(t, e, e.signer))
	if err != nil {
		t.Fatalf("sftp subsystem: %v", err)
	}
	defer func() { _ = client.Close() }()
	path := filepath.Join(svc.dir, "uploaded.txt")
	f, err := client.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(f, "from the laptop\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if got, rerr := os.ReadFile(path); rerr != nil || string(got) != "from the laptop\n" {
		t.Fatalf("uploaded file = %q %v", got, rerr)
	}
	back, err := client.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = back.Close() }()
	if got, rerr := io.ReadAll(back); rerr != nil || string(got) != "from the laptop\n" {
		t.Fatalf("downloaded file = %q %v", got, rerr)
	}
}
