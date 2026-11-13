package guest

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// MaxDiffEventBytes bounds one run.diff payload the git service sends.
	MaxDiffEventBytes = 16 << 20
	// MaxPatchBytes bounds a rendered patch, as the server's own.
	MaxPatchBytes = 64 << 20
	// ObjectsChunk is how much of a pack one Objects message carries.
	ObjectsChunk = 256 << 10
)

// GitReason names a failure of the server's git engine across the API, so
// the server can tell it apart from a broken call.
type GitReason string

const (
	ReasonSnapshotTreeMissing  GitReason = "snapshot-tree-missing"
	ReasonInvalidObjectID      GitReason = "invalid-object-id"
	ReasonSnapshotStorageLimit GitReason = "snapshot-storage-limit"
	ReasonCheckoutNotFound     GitReason = "checkout-not-found"
)

// GitError is a git service call that failed for Reason.
type GitError struct {
	Reason  GitReason
	Message string
}

func (e *GitError) Error() string { return e.Message }

var (
	// ErrPackTooLarge reports a pack longer than the caller's limit.
	ErrPackTooLarge = errors.New("guest: pack exceeds its size limit")
	// ErrVMNotRunning reports a VM known not to run, whose git service no
	// call can reach.
	ErrVMNotRunning = errors.New("guest: the VM is not running")
)

// Git is the client of one VM's git service.
type Git struct {
	conn *grpc.ClientConn
	api  guestpb.GitClient
}

// DialGit returns a client of the git service behind vsockPath, which
// connects and reconnects as Dial's does.
func DialGit(vsockPath string) (*Git, error) {
	conn, err := dial(vsockPath, GitPort)
	if err != nil {
		return nil, err
	}
	return &Git{conn: conn, api: guestpb.NewGitClient(conn)}, nil
}

// Close closes the connection.
func (g *Git) Close() error { return g.conn.Close() }

// Checkout is the identity of a checkout the configuration mounted under
// Key: on its first boot it gets Branch at Base, and Origin as its origin
// remote when set. UID and GID are the session user, who must own its
// files.
type Checkout struct {
	Key        string
	Workspace  string
	Branch     string
	Base       string
	BaseBranch string
	Origin     string
	UID, GID   int
}

// Setup sets up the checkouts, which must come before the session's first
// process: each gets its run branch on its first boot and its diff watch.
func (g *Git) Setup(ctx context.Context, checkouts []Checkout) error {
	req := &guestpb.SetupRequest{}
	for _, c := range checkouts {
		req.Checkouts = append(req.Checkouts, &guestpb.Checkout{
			Key: c.Key, Workspace: c.Workspace, Branch: c.Branch, Base: c.Base, BaseBranch: c.BaseBranch, Origin: c.Origin,
			Uid: uint32(c.UID), Gid: uint32(c.GID),
		})
	}
	if _, err := retry(ctx, func() (*guestpb.SetupReply, error) { return g.api.Setup(ctx, req) }); err != nil {
		return fmt.Errorf("guest: set up checkouts: %w", err)
	}
	return nil
}

// WatchEvent is one event of a checkout's diff watch. Exactly one of Diff,
// Head and Changed is set; Seq is nonzero for the first two, which need an
// acknowledgement.
type WatchEvent struct {
	Seq     uint64
	Diff    []byte
	Head    string
	Changed bool
}

// Watch is one checkout's open diff watch.
type Watch struct {
	stream grpc.BidiStreamingClient[guestpb.WatchAck, guestpb.WatchEvent]
}

// Watch opens the diff watch of the checkout named key, replacing any
// other watch of it. It lasts until ctx ends or the connection breaks.
func (g *Git) Watch(ctx context.Context, key string) (*Watch, error) {
	stream, err := g.api.Watch(ctx, grpc.MaxCallRecvMsgSize(MaxDiffEventBytes+4<<10))
	if err != nil {
		return nil, fmt.Errorf("guest: watch %s: %w", key, err)
	}
	if err := stream.Send(&guestpb.WatchAck{Key: key}); err != nil {
		return nil, fmt.Errorf("guest: watch %s: %w", key, err)
	}
	return &Watch{stream: stream}, nil
}

// Next waits for the next event.
func (w *Watch) Next() (WatchEvent, error) {
	ev, err := w.stream.Recv()
	if err != nil {
		return WatchEvent{}, err
	}
	out := WatchEvent{Seq: ev.GetSeq()}
	switch kind := ev.GetKind().(type) {
	case *guestpb.WatchEvent_Diff:
		out.Diff = kind.Diff
	case *guestpb.WatchEvent_Head:
		out.Head = truncate(kind.Head)
	case *guestpb.WatchEvent_Changed:
		out.Changed = true
	default:
		return WatchEvent{}, errors.New("guest: watch event of unknown kind")
	}
	if (out.Seq == 0) != out.Changed {
		return WatchEvent{}, errors.New("guest: watch event with a sequence number that does not match its kind")
	}
	return out, nil
}

// Ack acknowledges event seq; a non-nil failure refuses it.
func (w *Watch) Ack(seq uint64, failure error) error {
	ack := &guestpb.WatchAck{Seq: seq}
	if failure != nil {
		ack.Error = truncate(failure.Error())
	}
	return w.stream.Send(ack)
}

// Flush snapshots the checkout now and returns once its Watch delivered the
// snapshot.
func (g *Git) Flush(ctx context.Context, key string) error {
	if _, err := g.api.Flush(ctx, &guestpb.FlushRequest{Key: key}); err != nil {
		return gitErr("flush", key, err)
	}
	return nil
}

// Objects writes to w a pack of every object reachable from wants and from
// none of haves, failing with ErrPackTooLarge past limit bytes.
func (g *Git) Objects(ctx context.Context, key string, wants, haves []string, w io.Writer, limit int64) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := g.api.Objects(ctx, &guestpb.ObjectsRequest{Key: key, Wants: wants, Haves: haves})
	if err != nil {
		return gitErr("objects", key, err)
	}
	var written int64
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return gitErr("objects", key, err)
		}
		data := chunk.GetData()
		if written += int64(len(data)); written > limit {
			return fmt.Errorf("guest: objects %s: %w (%d bytes)", key, ErrPackTooLarge, limit)
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("guest: objects %s: %w", key, err)
		}
	}
}

// Head returns the commit the checkout's HEAD names.
func (g *Git) Head(ctx context.Context, key string) (string, error) {
	reply, err := g.api.Head(ctx, &guestpb.HeadRequest{Key: key})
	if err != nil {
		return "", gitErr("head", key, err)
	}
	return truncate(reply.GetCommit()), nil
}

// Patch is a rendered diff of a checkout, untrusted as everything the VM
// sends.
type Patch struct {
	Base      string
	Text      string
	Truncated bool
	ForkTree  string
}

// Patch renders the checkout's live diff from the snapshot tree from, or
// from its effective base when from is empty.
func (g *Git) Patch(ctx context.Context, key, from string, maxBytes int) (Patch, error) {
	maxBytes = min(max(maxBytes, 0), MaxPatchBytes)
	reply, err := g.api.Patch(ctx, &guestpb.PatchRequest{Key: key, From: from, MaxBytes: uint32(maxBytes)},
		grpc.MaxCallRecvMsgSize(MaxPatchBytes+64<<10))
	if err != nil {
		return Patch{}, gitErr("patch", key, err)
	}
	if maxBytes > 0 && len(reply.GetText()) > maxBytes {
		return Patch{}, fmt.Errorf("guest: patch %s: helper sent %d bytes for a %d-byte limit", key, len(reply.GetText()), maxBytes)
	}
	return Patch{Base: truncate(reply.GetBase()), Text: reply.GetText(), Truncated: reply.GetTruncated(), ForkTree: truncate(reply.GetForkTree())}, nil
}

// GitStatus is the status the git service returns for err: a GitError
// keeps its reason.
func GitStatus(err error) error {
	var failure *GitError
	if !errors.As(err, &failure) {
		return status.Error(codes.Internal, err.Error())
	}
	st, detailErr := status.New(codes.FailedPrecondition, failure.Message).WithDetails(&guestpb.GitFailure{Reason: string(failure.Reason)})
	if detailErr != nil {
		return status.Error(codes.FailedPrecondition, failure.Message)
	}
	return st.Err()
}

func gitErr(op, key string, err error) error {
	st := status.Convert(err)
	for _, detail := range st.Details() {
		if failure, ok := detail.(*guestpb.GitFailure); ok {
			return &GitError{Reason: GitReason(truncate(failure.GetReason())), Message: fmt.Sprintf("guest: %s %s: %s", op, key, truncate(st.Message()))}
		}
	}
	return fmt.Errorf("guest: %s %s: %w", op, key, err)
}
