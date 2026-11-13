package helper

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"github.com/3xDevOps/Aether/internal/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// dockerdID names the nested dockerd's process.
const dockerdID = "dockerd"

// service implements the API. Its system hooks are nil outside PID 1.
type service struct {
	guestpb.UnimplementedGuestServer
	procs     *processes
	cgroup    *sessionCgroup
	bootID    string
	bootError string
	powerOff  func() error
	setClock  func(time.Time) error
	freezer   *freezer

	bootOnce  sync.Once
	bootDone  chan struct{}
	bootReply *guestpb.BootReply
	bootErr   error
}

func newService(procs *processes, cgroup *sessionCgroup, bootID, bootError string) *service {
	return &service{procs: procs, cgroup: cgroup, bootID: bootID, bootError: bootError, bootDone: make(chan struct{})}
}

// newServer bounds requests and drops a server whose connection died
// silently, such as one cut off by a save and restore.
func newServer(s *service) *grpc.Server {
	server := newGRPCServer(8 << 20)
	guestpb.RegisterGuestServer(server, s)
	return server
}

func newGRPCServer(maxRecv int) *grpc.Server {
	return grpc.NewServer(
		grpc.MaxRecvMsgSize(maxRecv),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
	)
}

func (s *service) Info(context.Context, *guestpb.InfoRequest) (*guestpb.InfoReply, error) {
	return &guestpb.InfoReply{Protocol: guest.Protocol, Version: version.String(), BootId: s.bootID, BootError: s.bootError}, nil
}

// Boot runs on its own: a caller that gives up does not stop it, and every
// later call waits for the same result.
func (s *service) Boot(ctx context.Context, req *guestpb.BootRequest) (*guestpb.BootReply, error) {
	s.bootOnce.Do(func() {
		go func() {
			defer close(s.bootDone)
			s.bootReply, s.bootErr = s.boot(req)
		}()
	})
	select {
	case <-s.bootDone:
		return s.bootReply, s.bootErr
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}

func (s *service) Spawn(_ context.Context, req *guestpb.SpawnRequest) (*guestpb.SpawnReply, error) {
	spec := processSpec(req.GetProcess())
	switch spec.ID {
	case guest.MainID, guest.SetupID, dockerdID:
		return nil, status.Errorf(codes.InvalidArgument, "process ID %q is reserved", spec.ID)
	}
	if _, err := s.procs.spawn(spawnSpec{ProcessSpec: spec}); err != nil {
		return nil, rpcError(err)
	}
	return &guestpb.SpawnReply{}, nil
}

func (s *service) Read(ctx context.Context, req *guestpb.ReadRequest) (*guestpb.ReadReply, error) {
	p, err := s.procs.get(req.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	var out *output
	switch req.GetStream() {
	case guestpb.Stream_STREAM_STDOUT:
		out = p.stdout
	case guestpb.Stream_STREAM_STDERR:
		out = p.stderr
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown stream")
	}
	limit := int(req.GetMaxBytes())
	if limit <= 0 || limit > 64<<10 {
		limit = 64 << 10
	}
	offset, data, eof, err := out.readAt(ctx, req.GetOffset(), limit)
	if err != nil {
		return nil, rpcError(err)
	}
	return &guestpb.ReadReply{Offset: offset, Data: data, Eof: eof}, nil
}

func (s *service) Write(ctx context.Context, req *guestpb.WriteRequest) (*guestpb.WriteReply, error) {
	p, err := s.procs.get(req.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	n := 0
	if len(req.GetData()) > 0 {
		if n, err = p.write(ctx, req.GetData()); err != nil && n == 0 {
			return nil, rpcError(err)
		}
	}
	if req.GetClose() {
		if err := p.closeInput(); err != nil {
			return nil, rpcError(err)
		}
	}
	return &guestpb.WriteReply{Written: uint32(n)}, nil
}

func (s *service) Resize(_ context.Context, req *guestpb.ResizeRequest) (*guestpb.ResizeReply, error) {
	p, err := s.procs.get(req.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	cols, rows := req.GetSize().GetCols(), req.GetSize().GetRows()
	if cols == 0 || rows == 0 || cols > 65535 || rows > 65535 {
		return nil, status.Error(codes.InvalidArgument, "terminal dimensions must be between 1 and 65535")
	}
	if err := p.resize(guest.Size{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		return nil, rpcError(err)
	}
	return &guestpb.ResizeReply{}, nil
}

func (s *service) Signal(_ context.Context, req *guestpb.SignalRequest) (*guestpb.SignalReply, error) {
	sig := req.GetSignal()
	if sig <= 0 || sig > 64 {
		return nil, status.Errorf(codes.InvalidArgument, "signal %d is out of range", sig)
	}
	if err := s.procs.signal(req.GetId(), syscall.Signal(sig)); err != nil {
		return nil, rpcError(err)
	}
	return &guestpb.SignalReply{}, nil
}

func (s *service) Status(ctx context.Context, req *guestpb.StatusRequest) (*guestpb.StatusReply, error) {
	p, err := s.procs.get(req.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	if req.GetWait() {
		select {
		case <-p.done:
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
	if !p.exited() {
		return &guestpb.StatusReply{}, nil
	}
	p.reported.Store(true)
	return &guestpb.StatusReply{Exited: true, ExitCode: int32(p.code)}, nil
}

func (s *service) Stats(context.Context, *guestpb.StatsRequest) (*guestpb.StatsReply, error) {
	stats, err := readStats(s.cgroup)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read stats: %v", err)
	}
	return stats, nil
}

func (s *service) SetClock(_ context.Context, req *guestpb.SetClockRequest) (*guestpb.SetClockReply, error) {
	if s.setClock == nil {
		return nil, status.Error(codes.FailedPrecondition, "the helper is not PID 1")
	}
	if req.GetUnixNanos() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "time must be after 1970")
	}
	if err := s.setClock(time.Unix(0, req.GetUnixNanos())); err != nil {
		return nil, status.Errorf(codes.Internal, "set clock: %v", err)
	}
	return &guestpb.SetClockReply{}, nil
}

func (s *service) Sync(context.Context, *guestpb.SyncRequest) (*guestpb.SyncReply, error) {
	syscall.Sync()
	return &guestpb.SyncReply{}, nil
}

func (s *service) PowerOff(context.Context, *guestpb.PowerOffRequest) (*guestpb.PowerOffReply, error) {
	if s.powerOff == nil {
		return nil, status.Error(codes.FailedPrecondition, "the helper is not PID 1")
	}
	// sync waits on a frozen filesystem.
	if s.freezer != nil {
		_ = s.freezer.thawAny()
	}
	syscall.Sync()
	go func() {
		// The reply leaves before the VM goes down.
		time.Sleep(100 * time.Millisecond)
		_ = s.powerOff()
	}()
	return &guestpb.PowerOffReply{}, nil
}

func processSpec(p *guestpb.Process) guest.ProcessSpec {
	spec := guest.ProcessSpec{
		ID: p.GetId(), Argv: p.GetArgv(), Env: p.GetEnv(), User: p.GetUser(), Dir: p.GetDir(),
		Lossless: p.GetLossless(), NoNewPrivileges: p.GetNoNewPrivileges(),
	}
	if t := p.GetTerminal(); t != nil {
		spec.TTY = &guest.Size{Cols: uint16(min(t.GetCols(), 65535)), Rows: uint16(min(t.GetRows(), 65535))}
	}
	return spec
}

func rpcError(err error) error {
	var failed *execError
	switch {
	case errors.As(err, &failed):
		st, detailErr := status.New(codes.FailedPrecondition, failed.Error()).WithDetails(&guestpb.SpawnFailure{ExitCode: int32(failed.code)})
		if detailErr != nil {
			return status.Error(codes.FailedPrecondition, failed.Error())
		}
		return st.Err()
	case errors.Is(err, errUnknown):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, errExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, errOffset):
		return status.Error(codes.OutOfRange, err.Error())
	case errors.Is(err, errInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, errExited), errors.Is(err, errNoInput), errors.Is(err, errNoTerm), errors.Is(err, syscall.EPIPE), errors.Is(err, os.ErrClosed):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, os.ErrDeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	}
	return status.Error(codes.Internal, err.Error())
}
