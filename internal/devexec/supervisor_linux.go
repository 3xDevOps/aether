//go:build linux

package devexec

import (
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
	"time"

	"golang.org/x/sys/unix"
)

type controlCall struct {
	request Request
	response chan State
}

// Run owns exactly one child process group. The child inherits Docker's real
// terminal and becomes its foreground process group, so terminal-generated
// signals, job control and ioctl geometry all reach the actual application.
// This must run in a dedicated helper process: subreaping and signal handling
// are process-wide properties, not appropriate for the Aether server itself.
func Run(key string, argv []string) (int, error) {
	if key == "" || len(key) > 1024 || len(argv) == 0 || argv[0] == "" {
		return 125, errors.New("creation key and command are required")
	}
	if _, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS); err != nil {
		return 125, fmt.Errorf("owned command requires an inherited PTY: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return 125, fmt.Errorf("enable descendant reaping: %w", err)
	}
	if _, err := directChildren(); err != nil {
		return 125, fmt.Errorf("discover owned descendants: %w", err)
	}
	if err := os.Mkdir(stateDir(key), 0o700); err != nil {
		return 125, fmt.Errorf("reserve single-use execution identity: %w", err)
	}
	listener, err := net.Listen("unix", filepath.Join(stateDir(key), "control.sock"))
	if err != nil {
		return 125, err
	}
	defer listener.Close()
	calls := make(chan controlCall)
	done := make(chan struct{})
	defer close(done)
	go serveControl(listener, calls, done)

	signals := make(chan os.Signal, 16)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGWINCH)
	defer signal.Stop(signals)
	state := State{CreationKey: key}
	startup := time.NewTimer(StartupTimeout)
	defer startup.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var child *exec.Cmd
	var stopAt time.Time
	var killing bool
	var rootReaped bool
	var rootCode int

	finish := func(code int, cause error) (int, error) {
		state.Running, state.Exited, state.ExitCode = false, true, &code
		if cause != nil {
			state.Error = cause.Error()
		}
		return code, errors.Join(cause, saveState(state))
	}
	beginKill := func() error {
		if killing || child == nil {
			return nil
		}
		// The group leader is deliberately not reaped until after this kill.
		// Its PID therefore cannot have been reused for an unrelated group.
		if err := unix.Kill(-child.Process.Pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
		killing = true
		return nil
	}
	// Any infrastructure error after Start still cleans up our descendants.
	defer func() {
		if child != nil {
			if !rootReaped {
				_ = beginKill()
			}
			for {
				_ = signalChildren(unix.SIGKILL)
				var status unix.WaitStatus
				pid, waitErr := unix.Wait4(-1, &status, unix.WNOHANG, nil)
				if errors.Is(waitErr, unix.ECHILD) {
					break
				}
				if waitErr != nil && !errors.Is(waitErr, unix.EINTR) {
					break
				}
				if pid == 0 {
					time.Sleep(20 * time.Millisecond)
				}
			}
			_ = child.Process.Release()
		}
	}()

	for {
		select {
		case call := <-calls:
			req := call.request
			response := state
			if req.ExecID == "" || (state.ExecID != "" && state.ExecID != req.ExecID) {
				response.Error = "execution identity mismatch"
				call.response <- response
				continue
			}
			switch req.Action {
			case "start":
				if child == nil {
					state.ExecID = req.ExecID
					child = exec.Command(argv[0], argv[1:]...)
					child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
					child.SysProcAttr = &syscall.SysProcAttr{
						Setpgid: true, Foreground: true, Ctty: int(os.Stdin.Fd()),
						Pdeathsig: syscall.SIGKILL,
					}
					if err := child.Start(); err != nil {
						child = nil
						code := 126
						if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
							code = 127
						}
						_, saveErr := finish(code, err)
						call.response <- state
						return code, saveErr
					}
					startup.Stop()
					state.Running = true
					if err := saveState(state); err != nil {
						response = state
						response.Error = err.Error()
						call.response <- response
						return 125, err
					}
				}
				response = state
			case "status":
				response = state
			case "stop":
				if child == nil {
					state.ExecID = req.ExecID
					code, err := finish(125, nil)
					call.response <- state
					return code, err
				}
				if req.GraceMillis < 0 || req.GraceMillis > 60000 {
					response.Error = "stop grace must be between 0 and 60000 milliseconds"
					break
				}
				deadline := time.Now().Add(time.Duration(req.GraceMillis) * time.Millisecond)
				if stopAt.IsZero() || deadline.Before(stopAt) {
					stopAt = deadline
				}
				if !killing {
					if err := unix.Kill(-child.Process.Pid, unix.SIGTERM); err != nil && !errors.Is(err, unix.ESRCH) {
						response.Error = err.Error()
					}
				}
			default:
				response.Error = "unknown execution control operation"
			}
			call.response <- response
		case received := <-signals:
			if child == nil {
				return finish(128+int(received.(syscall.Signal)), nil)
			}
			if !killing {
				_ = unix.Kill(-child.Process.Pid, received.(syscall.Signal))
			}
			if received != syscall.SIGWINCH && stopAt.IsZero() {
				stopAt = time.Now().Add(3 * time.Second)
			}
		case <-startup.C:
			if child == nil {
				return finish(125, errors.New("execution was not claimed before startup deadline"))
			}
		case <-tick.C:
			if child == nil {
				continue
			}
			if !killing {
				var info unix.Siginfo
				if err := unix.Waitid(unix.P_PID, child.Process.Pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil); err != nil && !errors.Is(err, unix.EINTR) {
					return 125, fmt.Errorf("observe owned command exit: %w", err)
				}
				if info.Signo != 0 || (!stopAt.IsZero() && !time.Now().Before(stopAt)) {
					if err := beginKill(); err != nil {
						return 125, err
					}
				}
			}
			if !killing {
				continue
			}
			// Subreaping adopts double-forked/session-detached descendants.
			// Only direct, unreaped children are signalled, so even a PID
			// recycled elsewhere in the container can never be targeted.
			if err := signalChildren(unix.SIGKILL); err != nil {
				return 125, err
			}
			for {
				var status unix.WaitStatus
				pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
				if errors.Is(err, unix.ECHILD) {
					if !rootReaped {
						return 125, errors.New("owned command exit status unavailable")
					}
					return finish(rootCode, nil)
				}
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if err != nil {
					return 125, err
				}
				if pid == 0 {
					break
				}
				if pid == child.Process.Pid {
					rootReaped = true
					rootCode = status.ExitStatus()
					if status.Signaled() {
						rootCode = 128 + int(status.Signal())
					}
				}
			}
		}
	}
}

func serveControl(listener net.Listener, calls chan<- controlCall, done <-chan struct{}) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(StartupTimeout))
		var request Request
		if err := json.NewDecoder(io.LimitReader(conn, 16<<10)).Decode(&request); err == nil {
			call := controlCall{request: request, response: make(chan State, 1)}
			select {
			case calls <- call:
				select {
				case state := <-call.response:
					_ = json.NewEncoder(conn).Encode(state)
				case <-done:
					// A final response may have been queued just before exit.
					select {
					case state := <-call.response:
						_ = json.NewEncoder(conn).Encode(state)
					default:
					}
				}
			case <-done:
			}
		}
		_ = conn.Close()
	}
}

func directChildren() ([]int, error) {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return nil, err
	}
	var children []int
	for _, task := range tasks {
		data, err := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
		if errors.Is(err, os.ErrNotExist) {
			continue // A Go runtime thread exited during enumeration.
		}
		if err != nil {
			return nil, err
		}
		for field := range strings.FieldsSeq(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 {
				return nil, errors.New("invalid child process identity")
			}
			children = append(children, pid)
		}
	}
	return children, nil
}

func signalChildren(signal unix.Signal) error {
	children, err := directChildren()
	if err != nil {
		return err
	}
	for _, pid := range children {
		if err := unix.Kill(pid, signal); err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
	}
	return nil
}
