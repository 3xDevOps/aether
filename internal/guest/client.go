package guest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Protocol is the API version this build speaks. A helper reporting another
// one, such as an older helper in a VM restored from a save, is refused with
// ErrIncompatible: the session then needs a cold start.
const Protocol = 1

const (
	// maxReply bounds every message the helper sends. Read replies, the
	// largest, carry at most readChunk bytes.
	maxReply = 1 << 20
	// readChunk and writeChunk bound one Read and one Write call.
	readChunk  = 64 << 10
	writeChunk = 256 << 10
	// infoLimit bounds each string the helper sends that the server shows.
	infoLimit = 4 << 10
)

var (
	// ErrIncompatible reports a helper that speaks another Protocol.
	ErrIncompatible = errors.New("guest: helper speaks an incompatible protocol")
	// ErrNotFound reports a process the helper has no record of: it is not
	// running.
	ErrNotFound = errors.New("guest: no such process")
)

// SetupError reports a setup script that exited nonzero; the main process
// never started. Output is the raw end of what the script printed, so the
// caller masks secrets before showing it. Cut reports that Output lost its
// beginning.
type SetupError struct {
	ExitCode int
	Output   string
	Cut      bool
}

func (e *SetupError) Error() string {
	return fmt.Sprintf("guest: setup script exited %d", e.ExitCode)
}

// StartError reports a program that could not be executed: Code is 127 when
// it was not found and 126 when it could not run.
type StartError struct {
	Code int
	Err  error
}

func (e *StartError) Error() string { return e.Err.Error() }
func (e *StartError) Unwrap() error { return e.Err }

// Info describes the helper of one boot.
type Info struct {
	Protocol int
	Version  string
	// BootID changes on every boot and survives a save and restore.
	BootID string
	// BootError is the first PID 1 duty that failed, empty when none did.
	BootError string
}

// Size is a terminal's geometry.
type Size struct {
	Cols, Rows uint16
}

// ProcessSpec describes one process. Env is the complete environment; the
// helper adds HOME from the user's passwd entry and Docker's default PATH
// when it lacks them. User is "uid:gid", "uid" or empty for root.
type ProcessSpec struct {
	// ID is chosen by the caller and used once per boot, so a spawn lost
	// with its reply is found again under the same ID.
	ID   string
	Argv []string
	Env  []string
	User string
	Dir  string
	// TTY, when set, runs the process on a new terminal of this size.
	TTY *Size
	// Lossless blocks the process while its unread output fills the
	// helper's window, instead of dropping the oldest bytes.
	Lossless bool
	// NoNewPrivileges is Docker's no-new-privileges for this process and
	// its descendants: no set-user-ID program or file capability raises
	// them.
	NoNewPrivileges bool
}

// BootSpec starts a session: dockerd when NestedDocker is set and the image
// has it, SetupScript under Main's user, environment and directory, then
// Main. PidsLimit caps the tasks of every process the helper starts; 0 is
// unlimited.
type BootSpec struct {
	Main         ProcessSpec
	SetupScript  string
	NestedDocker bool
	PidsLimit    int64
}

// State is a process's lifecycle.
type State struct {
	Exited bool
	// ExitCode is 128 plus the signal number for a process a signal killed.
	ExitCode int
}

// Stats are the VM's counters; Pids counts the tasks PidsLimit applies to.
type Stats struct {
	CPUUsageNanos    uint64
	MemoryBytes      uint64
	MemoryLimitBytes uint64
	Pids             uint64
	PidsLimit        uint64
	OOMKills         uint64
}

// Client talks to the helper of one VM. Calls wait for the helper while the
// VM boots or is restored; bound them with a context.
type Client struct {
	conn *grpc.ClientConn
	api  guestpb.GuestClient
}

// Dial returns a client of the helper behind vsockPath, the host side of a
// Cloud Hypervisor vsock device. It does not connect yet: the first call
// does, and the client reconnects by itself after the VM is restored.
func Dial(vsockPath string) (*Client, error) {
	conn, err := grpc.NewClient("passthrough:///guest",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return dialVsock(ctx, vsockPath, APIPort)
		}),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: 50 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: time.Second},
			MinConnectTimeout: 5 * time.Second,
		}),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true), grpc.MaxCallRecvMsgSize(maxReply)),
	)
	if err != nil {
		return nil, fmt.Errorf("guest: %w", err)
	}
	return &Client{conn: conn, api: guestpb.NewGuestClient(conn)}, nil
}

// dialVsock connects to port in the VM through Cloud Hypervisor's hybrid
// vsock: a CONNECT line on the device's Unix socket, answered by OK.
func dialVsock(ctx context.Context, socket string, port uint32) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if _, err = fmt.Fprintf(conn, "CONNECT %d\n", port); err == nil {
		err = readOK(conn)
	}
	if err == nil {
		err = conn.SetDeadline(time.Time{})
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("vsock connect to port %d: %w", port, err)
	}
	return conn, nil
}

// readOK reads the reply line one byte at a time: the helper's first bytes
// may follow it in the same segment.
func readOK(conn net.Conn) error {
	var line []byte
	b := make([]byte, 1)
	for len(line) < 64 {
		if _, err := io.ReadFull(conn, b); err != nil {
			return err
		}
		if b[0] == '\n' {
			if !bytes.HasPrefix(line, []byte("OK ")) {
				return fmt.Errorf("refused: %q", line)
			}
			return nil
		}
		line = append(line, b[0])
	}
	return errors.New("reply line too long")
}

// Close closes the connection; processes in the VM keep running.
func (c *Client) Close() error { return c.conn.Close() }

// Ping waits for the helper and checks that it speaks Protocol.
func (c *Client) Ping(ctx context.Context) (Info, error) {
	reply, err := retry(ctx, func() (*guestpb.InfoReply, error) {
		return c.api.Info(ctx, &guestpb.InfoRequest{})
	})
	if err != nil {
		return Info{}, fmt.Errorf("guest: info: %w", err)
	}
	info := Info{
		Protocol:  int(reply.GetProtocol()),
		Version:   truncate(reply.GetVersion()),
		BootID:    truncate(reply.GetBootId()),
		BootError: truncate(reply.GetBootError()),
	}
	if info.Protocol != Protocol {
		return info, fmt.Errorf("%w: helper %s speaks protocol %d, this server %d", ErrIncompatible, info.Version, info.Protocol, Protocol)
	}
	return info, nil
}

// Boot starts the session and returns once its main process runs, or with
// *SetupError when the setup script failed. Every later call, including one
// from a restarted server, returns the first call's result.
func (c *Client) Boot(ctx context.Context, spec BootSpec) error {
	req := &guestpb.BootRequest{
		Main: spec.Main.proto(), SetupScript: spec.SetupScript, NestedDocker: spec.NestedDocker, PidsLimit: spec.PidsLimit,
	}
	reply, err := retry(ctx, func() (*guestpb.BootReply, error) { return c.api.Boot(ctx, req) })
	if err != nil {
		return fmt.Errorf("guest: boot: %w", err)
	}
	code := reply.GetSetupExitCode()
	if code < 0 || code > 255 {
		return fmt.Errorf("guest: boot: helper reports setup exit code %d", code)
	}
	if code != 0 {
		output, cut := reply.GetSetupOutput(), reply.GetSetupOutputCut()
		if len(output) > SetupOutputLimit {
			output, cut = output[len(output)-SetupOutputLimit:], true
		}
		return &SetupError{ExitCode: int(code), Output: string(output), Cut: cut}
	}
	return nil
}

// Spawn starts a process. A spec whose ID was already used fails, also when
// the earlier spawn's reply was lost; *StartError reports a program that
// could not be executed.
func (c *Client) Spawn(ctx context.Context, spec ProcessSpec) (*Process, error) {
	_, err := c.api.Spawn(ctx, &guestpb.SpawnRequest{Process: spec.proto()})
	if err != nil {
		for _, detail := range status.Convert(err).Details() {
			if failure, ok := detail.(*guestpb.SpawnFailure); ok && (failure.GetExitCode() == 126 || failure.GetExitCode() == 127) {
				return nil, &StartError{Code: int(failure.GetExitCode()), Err: fmt.Errorf("guest: spawn %s: %s", spec.ID, truncate(status.Convert(err).Message()))}
			}
		}
		return nil, fmt.Errorf("guest: spawn %s: %w", spec.ID, err)
	}
	return c.Process(spec.ID), nil
}

// Process returns a handle on the process named id, such as one a previous
// server spawned. It does not check that the process exists.
func (c *Client) Process(id string) *Process {
	return &Process{c: c, id: id}
}

// Stats samples the VM's counters.
func (c *Client) Stats(ctx context.Context) (Stats, error) {
	r, err := retry(ctx, func() (*guestpb.StatsReply, error) { return c.api.Stats(ctx, &guestpb.StatsRequest{}) })
	if err != nil {
		return Stats{}, fmt.Errorf("guest: stats: %w", err)
	}
	return Stats{
		CPUUsageNanos: r.GetCpuUsageNanos(), MemoryBytes: r.GetMemoryBytes(), MemoryLimitBytes: r.GetMemoryLimitBytes(),
		Pids: r.GetPids(), PidsLimit: r.GetPidsLimit(), OOMKills: r.GetOomKills(),
	}, nil
}

// SyncClock sets the VM's clock to now, which a restored VM needs: its clock
// stopped while it was saved.
func (c *Client) SyncClock(ctx context.Context, now time.Time) error {
	_, err := retry(ctx, func() (*guestpb.SetClockReply, error) {
		return c.api.SetClock(ctx, &guestpb.SetClockRequest{UnixNanos: now.UnixNano()})
	})
	if err != nil {
		return fmt.Errorf("guest: set clock: %w", err)
	}
	return nil
}

// PowerOff flushes the VM's disks and powers it off. It returns once the
// disks are flushed.
func (c *Client) PowerOff(ctx context.Context) error {
	if _, err := c.api.PowerOff(ctx, &guestpb.PowerOffRequest{}); err != nil {
		return fmt.Errorf("guest: power off: %w", err)
	}
	return nil
}

// Process is a handle on one process in the VM.
type Process struct {
	c  *Client
	id string
}

func (p *Process) ID() string { return p.id }

// Write writes p to the process's input, or to its terminal, and reports
// how much the process took before ctx ended.
func (p *Process) Write(ctx context.Context, b []byte) (int, error) {
	written := 0
	for len(b) > 0 {
		chunk := b[:min(len(b), writeChunk)]
		reply, err := p.c.api.Write(ctx, &guestpb.WriteRequest{Id: p.id, Data: chunk})
		if err != nil {
			return written, p.err("write", err)
		}
		n := int(reply.GetWritten())
		if n > len(chunk) {
			return written, fmt.Errorf("guest: write %s: helper reports %d bytes written of %d", p.id, n, len(chunk))
		}
		written += n
		if n < len(chunk) {
			return written, fmt.Errorf("guest: write %s: %w", p.id, io.ErrShortWrite)
		}
		b = b[n:]
	}
	return written, nil
}

// CloseStdin closes the process's input; a process on a terminal has none.
func (p *Process) CloseStdin(ctx context.Context) error {
	if _, err := p.c.api.Write(ctx, &guestpb.WriteRequest{Id: p.id, Close: true}); err != nil {
		return p.err("close input", err)
	}
	return nil
}

// Resize sets the geometry of the process's terminal.
func (p *Process) Resize(ctx context.Context, size Size) error {
	if size.Cols == 0 || size.Rows == 0 {
		return errors.New("guest: terminal dimensions must be at least 1")
	}
	_, err := retry(ctx, func() (*guestpb.ResizeReply, error) {
		return p.c.api.Resize(ctx, &guestpb.ResizeRequest{Id: p.id, Size: size.proto()})
	})
	return p.err("resize", err)
}

// Signal sends sig to the process.
func (p *Process) Signal(ctx context.Context, sig syscall.Signal) error {
	_, err := p.c.api.Signal(ctx, &guestpb.SignalRequest{Id: p.id, Signal: int32(sig)})
	return p.err("signal", err)
}

// Status reports whether the process exited, without waiting.
func (p *Process) Status(ctx context.Context) (State, error) {
	return p.status(ctx, false)
}

// Wait blocks until the process exits and returns its exit code.
func (p *Process) Wait(ctx context.Context) (int, error) {
	state, err := p.status(ctx, true)
	return state.ExitCode, err
}

func (p *Process) status(ctx context.Context, wait bool) (State, error) {
	reply, err := retry(ctx, func() (*guestpb.StatusReply, error) {
		return p.c.api.Status(ctx, &guestpb.StatusRequest{Id: p.id, Wait: wait})
	})
	if err != nil {
		return State{}, p.err("status", err)
	}
	code := reply.GetExitCode()
	if code < 0 || code > 255 {
		return State{}, fmt.Errorf("guest: status %s: helper reports exit code %d", p.id, code)
	}
	return State{Exited: reply.GetExited(), ExitCode: int(code)}, nil
}

func (p *Process) err(op string, err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.NotFound {
		return fmt.Errorf("guest: %s %s: %w", op, p.id, ErrNotFound)
	}
	return fmt.Errorf("guest: %s %s: %w", op, p.id, err)
}

// retry repeats an idempotent call that a broken connection cut off, such
// as one in flight when the VM was saved. A call that has not started yet
// already waits for the connection.
func retry[T any](ctx context.Context, call func() (T, error)) (T, error) {
	for {
		reply, err := call()
		if status.Code(err) != codes.Unavailable || ctx.Err() != nil {
			return reply, err
		}
	}
}

func (s ProcessSpec) proto() *guestpb.Process {
	p := &guestpb.Process{Id: s.ID, Argv: s.Argv, Env: s.Env, User: s.User, Dir: s.Dir, Lossless: s.Lossless, NoNewPrivileges: s.NoNewPrivileges}
	if s.TTY != nil {
		p.Terminal = s.TTY.proto()
	}
	return p
}

func (s Size) proto() *guestpb.Size {
	return &guestpb.Size{Cols: uint32(s.Cols), Rows: uint32(s.Rows)}
}

// truncate caps a string the helper sent.
func truncate(s string) string {
	if len(s) <= infoLimit {
		return s
	}
	return strings.ToValidUTF8(s[:infoLimit], "") + "...(" + strconv.Itoa(len(s)-infoLimit) + " more bytes)"
}
