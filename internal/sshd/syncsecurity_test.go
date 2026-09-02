package sshd

// Adversarial coverage for the aether-sync bridge: a client that speaks
// the wire protocol by hand instead of through internal/overlay, so the
// initialization frame and the conflict report carry whatever a custom
// client would send rather than what the cooperative one does.

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mutagen-io/mutagen/pkg/encoding"
	"github.com/mutagen-io/mutagen/pkg/synchronization"
	"github.com/mutagen-io/mutagen/pkg/synchronization/compression"
	"github.com/mutagen-io/mutagen/pkg/synchronization/core"
	"github.com/mutagen-io/mutagen/pkg/synchronization/core/ignore"
	"github.com/mutagen-io/mutagen/pkg/synchronization/endpoint/remote"
	"github.com/mutagen-io/mutagen/pkg/synchronization/rsync"
	"golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/proto"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/overlay"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// hostileSync is a client that speaks the aether-sync wire protocol by
// hand instead of through internal/overlay, so the initialization frame
// can carry whatever a custom client would send. Everything the real
// client pins server-friendly - session identifier, configuration,
// endpoint role - is a parameter here.
type hostileSync struct {
	pipe *subsystemPipe
	r    *bufio.Reader
	enc  *encoding.ProtobufEncoder
	dec  *encoding.ProtobufDecoder
}

// dialHostileSync opens the subsystem, sends the header, and completes
// the compression handshake, stopping just before the init frame.
func dialHostileSync(t *testing.T, e *testEnv, signer ssh.Signer, run domain.RunID) *hostileSync {
	t.Helper()
	client, err := e.dialWith(signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	pipe := openSubsystem(t, client, protocol.SubsystemSync, nil)
	header, _ := json.Marshal(protocol.SyncRequest{RunID: string(run), Force: true})
	if _, werr := pipe.Write(append(header, '\n')); werr != nil {
		t.Fatalf("write header: %v", werr)
	}
	r := bufio.NewReader(pipe)
	var ack protocol.SyncResponse
	readJSONLine(t, r, &ack)
	if !ack.OK {
		t.Fatalf("ack = %+v, want ok", ack)
	}
	// Compression handshake: "none", then the server's acceptance byte.
	if _, werr := pipe.Write([]byte{byte(compression.Algorithm_AlgorithmNone)}); werr != nil {
		t.Fatalf("write algorithm: %v", werr)
	}
	reply, rerr := r.ReadByte()
	if rerr != nil || reply != 1 {
		t.Fatalf("compression reply = %d (err %v), want 1", reply, rerr)
	}
	return &hostileSync{
		pipe: pipe,
		r:    r,
		enc:  encoding.NewProtobufEncoder(pipe),
		dec:  encoding.NewProtobufDecoder(r),
	}
}

// init sends an initialization frame verbatim and reads the response.
func (h *hostileSync) init(req *remote.InitializeSynchronizationRequest) (*remote.InitializeSynchronizationResponse, error) {
	if err := h.enc.Encode(req); err != nil {
		return nil, err
	}
	resp := &remote.InitializeSynchronizationResponse{}
	if err := h.dec.Decode(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// scan drives one endpoint scan and returns the served snapshot: the
// cheapest way to observe what the server-side endpoint considers to be
// inside its root, and the call the endpoint requires before any write.
func (h *hostileSync) scan() (*core.Snapshot, error) {
	engine := rsync.NewEngine()
	baseline, err := proto.MarshalOptions{Deterministic: true}.Marshal(&core.Snapshot{PreservesExecutability: true})
	if err != nil {
		return nil, err
	}
	signature := engine.BytesSignature(baseline, 0)
	if err = h.enc.Encode(&remote.EndpointRequest{
		Scan: &remote.ScanRequest{BaselineSnapshotSignature: signature, Full: true},
	}); err != nil {
		return nil, err
	}
	// The completion request must follow the response: mutagen preempts
	// the in-flight operation when it arrives first.
	resp := &remote.ScanResponse{}
	if err = h.dec.Decode(resp); err != nil {
		return nil, err
	}
	if err = h.enc.Encode(&remote.ScanCompletionRequest{}); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	patched, err := engine.PatchBytes(baseline, signature, resp.SnapshotDelta)
	if err != nil {
		return nil, err
	}
	snapshot := &core.Snapshot{}
	if err = proto.Unmarshal(patched, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// transition asks the endpoint to apply changes to its root. Deletions
// need no staged content, so this is a write the hostile client can drive
// on its own.
func (h *hostileSync) transition(changes []*core.Change) (*remote.TransitionResponse, error) {
	if err := h.enc.Encode(&remote.EndpointRequest{
		Transition: &remote.TransitionRequest{Transitions: changes},
	}); err != nil {
		return nil, err
	}
	resp := &remote.TransitionResponse{}
	if err := h.dec.Decode(resp); err != nil {
		return nil, err
	}
	if err := h.enc.Encode(&remote.TransitionCompletionRequest{}); err != nil {
		return nil, err
	}
	return resp, nil
}

func (h *hostileSync) Close() { _ = h.pipe.Close() }

// hostileInit is a well-formed initialization request whose every
// server-relevant field is chosen by the attacker.
func hostileInit(root, session string, cfg *synchronization.Configuration, alpha bool) *remote.InitializeSynchronizationRequest {
	return &remote.InitializeSynchronizationRequest{
		Root:          root,
		Session:       session,
		Version:       synchronization.DefaultVersion,
		Configuration: cfg,
		Alpha:         alpha,
	}
}

// mutagenDataDir points mutagen's per-process data directory (caches,
// staging) at a scratch dir, so a test can see what the server-side
// endpoint wrote and where.
func mutagenDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MUTAGEN_DATA_DIRECTORY", dir)
	return dir
}

// Finding 1: the client controls Session, which mutagen joins onto its
// caches and staging directories and then writes to. The server must
// supply it, so a traversal value never reaches those joins.
func TestSyncPinsSessionIdentifierAgainstTraversal(t *testing.T) {
	dataDir := mutagenDataDir(t)
	e, worktree := syncEnv(t)
	escape := filepath.Join(t.TempDir(), "owned")
	if err := os.MkdirAll(escape, 0o755); err != nil {
		t.Fatal(err)
	}

	// A session identifier that climbs out of the caches directory and
	// into a directory of the attacker's choosing.
	rel, err := filepath.Rel(filepath.Join(dataDir, "caches"), escape)
	if err != nil {
		t.Fatal(err)
	}
	traversal := filepath.ToSlash(rel) + "/pwned"

	h := dialHostileSync(t, e, e.signer, e.run.ID)
	defer h.Close()
	resp, err := h.init(hostileInit("/nonexistent", traversal, &synchronization.Configuration{}, false))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("init refused: %s", resp.Error)
	}

	// Drive real endpoint work so every session-derived path is computed
	// and the cache is written.
	if _, serr := h.scan(); serr != nil {
		t.Fatalf("scan: %v", serr)
	}
	if err := os.WriteFile(filepath.Join(worktree, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, serr := h.scan(); serr != nil {
		t.Fatalf("second scan: %v", serr)
	}

	// The identifier the server actually used is the run-derived one. The
	// endpoint writes its cache from a background goroutine that stops on
	// shutdown, so wait for the write before closing the session.
	want, werr := syncSessionID(e.run.ID)
	if werr != nil {
		t.Fatal(werr)
	}
	waitForPath(t, filepath.Join(dataDir, "caches", want+"_beta"))

	h.Close()

	// Nothing may appear under the attacker-named directory: not the
	// cache file, not a staging root, nothing.
	entries, rerr := os.ReadDir(escape)
	if rerr != nil {
		t.Fatalf("read escape dir: %v", rerr)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, ent := range entries {
			names = append(names, ent.Name())
		}
		t.Fatalf("session traversal wrote outside the data directory: %v", names)
	}
}

// syncSessionID must reject anything that is not plainly one path
// element, so a future run-ID format cannot reintroduce the traversal.
func TestSyncSessionIDRejectsPathElements(t *testing.T) {
	for _, bad := range []domain.RunID{
		"", "..", "../../tmp/owned", "a/b", `a\b`, "a.b", "a:b", "a b",
		domain.RunID(strings.Repeat("a", 65)),
	} {
		if got, err := syncSessionID(bad); err == nil {
			t.Errorf("syncSessionID(%q) = %q, want error", bad, got)
		}
	}
	got, err := syncSessionID("01jq2wz8xa9c")
	if err != nil {
		t.Fatalf("syncSessionID on a real run id: %v", err)
	}
	if strings.ContainsAny(got, `/\:`) || got == "" {
		t.Fatalf("syncSessionID = %q, want one opaque path element", got)
	}
}

// Finding 2: mutagen only ignores VCS directories when the request asks
// it to, and its own default is to propagate them. A client asking for
// propagation must not get it: .git is invisible to the endpoint no
// matter what the init frame says.
func TestSyncPinsConfigurationAgainstHostileClient(t *testing.T) {
	mutagenDataDir(t)
	e, worktree := syncEnv(t)
	gitDir := filepath.Join(worktree, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "visible.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := dialHostileSync(t, e, e.signer, e.run.ID)
	defer h.Close()
	// The security-relevant knobs set the wrong way: propagate VCS state
	// and stamp server-created entries with client-chosen permissions.
	// Raw symlinks get their own test below (they are unrepresentable on
	// Windows, and mutagen refuses them before the .git assertion here
	// could run); client-chosen owner and group are covered by
	// TestSyncPinOverwritesEverySecurityRelevantField, for the same
	// reason.
	resp, err := h.init(hostileInit(worktree, "hostile", &synchronization.Configuration{
		IgnoreVCSMode:   ignore.IgnoreVCSMode_IgnoreVCSModePropagate,
		PermissionsMode: core.PermissionsMode_PermissionsModeManual,
		DefaultFileMode: 0o777,
	}, false))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("init refused: %s", resp.Error)
	}

	snapshot, serr := h.scan()
	if serr != nil {
		t.Fatalf("scan: %v", serr)
	}
	if snapshot.Content == nil {
		t.Fatal("empty snapshot; the endpoint is not rooted at the worktree")
	}
	if v := snapshot.Content.Contents["visible.txt"]; v == nil || v.Kind != core.EntryKind_File {
		t.Fatalf("worktree file missing from snapshot: %v", snapshot.Content.Contents)
	}
	// The whole point: .git is not synchronizable content, so it can be
	// neither read out of the worktree nor written back into it. An
	// ignored directory is reported as Untracked rather than omitted.
	if g := snapshot.Content.Contents[".git"]; g != nil && g.Kind != core.EntryKind_Untracked {
		t.Fatalf(".git is visible to the sync as %v: the client's IgnoreVCSMode won", g.Kind)
	}

	// A client that nonetheless asks to delete .git is refused, because
	// the endpoint's ignorer never saw it.
	tresp, terr := h.transition([]*core.Change{{
		Path: ".git",
		Old: &core.Entry{Kind: core.EntryKind_Directory, Contents: map[string]*core.Entry{
			"HEAD": {Kind: core.EntryKind_File, Digest: []byte("whatever")},
		}},
	}})
	if terr != nil {
		t.Fatalf("transition: %v", terr)
	}
	if len(tresp.Problems) == 0 {
		t.Fatal("transition deleting .git reported no problem")
	}
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
		t.Fatalf(".git/HEAD destroyed by a hostile transition: %v", err)
	}
}

// The pinned configuration is what mutagen receives, field by field. The
// scan above proves the VCS half end to end; this covers the rest of the
// policy without needing a live filesystem effect for each knob.
func TestSyncPinOverwritesEverySecurityRelevantField(t *testing.T) {
	s := &rootPinningStream{root: "/srv/worktree", session: "aether-run1"}
	req := hostileInit("/etc", "../../owned", &synchronization.Configuration{
		IgnoreVCSMode:        ignore.IgnoreVCSMode_IgnoreVCSModePropagate,
		SymbolicLinkMode:     core.SymbolicLinkMode_SymbolicLinkModePOSIXRaw,
		StageMode:            synchronization.StageMode_StageModeNeighboring,
		SynchronizationMode:  core.SynchronizationMode_SynchronizationModeOneWayReplica,
		PermissionsMode:      core.PermissionsMode_PermissionsModeManual,
		DefaultFileMode:      0o777,
		DefaultDirectoryMode: 0o777,
		DefaultOwner:         "id:0",
		DefaultGroup:         "id:0",
		CompressionAlgorithm: compression.Algorithm_AlgorithmZstandard,
		Ignores:              []string{"*.aether-conflict"},
	}, true)
	s.pin(req)

	if req.Root != "/srv/worktree" || req.Session != "aether-run1" || req.Alpha {
		t.Fatalf("root/session/alpha = %q/%q/%v", req.Root, req.Session, req.Alpha)
	}
	c := req.Configuration
	if c.IgnoreVCSMode != ignore.IgnoreVCSMode_IgnoreVCSModeIgnore {
		t.Errorf("IgnoreVCSMode = %v, want Ignore", c.IgnoreVCSMode)
	}
	if c.SymbolicLinkMode != core.SymbolicLinkMode_SymbolicLinkModePortable {
		t.Errorf("SymbolicLinkMode = %v, want Portable", c.SymbolicLinkMode)
	}
	if c.StageMode != synchronization.StageMode_StageModeMutagen {
		t.Errorf("StageMode = %v, want Mutagen", c.StageMode)
	}
	if c.SynchronizationMode != core.SynchronizationMode_SynchronizationModeTwoWaySafe {
		t.Errorf("SynchronizationMode = %v, want TwoWaySafe", c.SynchronizationMode)
	}
	if c.PermissionsMode != core.PermissionsMode_PermissionsModePortable {
		t.Errorf("PermissionsMode = %v, want Portable", c.PermissionsMode)
	}
	if c.DefaultFileMode != 0 || c.DefaultDirectoryMode != 0 || c.DefaultOwner != "" || c.DefaultGroup != "" {
		t.Errorf("client ownership/permission defaults survived: %+v", c)
	}
	if c.CompressionAlgorithm != compression.Algorithm_AlgorithmNone {
		t.Errorf("CompressionAlgorithm = %v, want None", c.CompressionAlgorithm)
	}
	// Ignores are subtractive and must match the client's alpha endpoint,
	// so they are deliberately preserved.
	if len(c.Ignores) != 1 || c.Ignores[0] != "*.aether-conflict" {
		t.Errorf("Ignores = %v, want the client's list preserved", c.Ignores)
	}
}

// Finding 3: authorization is a snapshot taken before ServeEndpoint. A
// client that keeps its stream open must lose access the moment policy
// changes - here the run is protected mid-stream, which denies Steer to
// a non-owner collaborator.
func TestSyncRevokesStreamWhenAuthorizationIsLost(t *testing.T) {
	mutagenDataDir(t)
	e := newTestEnv(t, func(c *Config) { c.revalidateInterval = 50 * time.Millisecond })
	worktree := t.TempDir()
	e.run.Worktree = worktree
	e.run.Status = domain.RunNeedsAttention
	if err := e.store.UpdateRun(context.Background(), e.run); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "doomed.txt"), []byte("still here"), 0o644); err != nil {
		t.Fatal(err)
	}
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	h := dialHostileSync(t, e, collab, e.run.ID)
	defer h.Close()
	resp, err := h.init(hostileInit(worktree, "hostile", &synchronization.Configuration{}, false))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("init refused: %s", resp.Error)
	}
	snapshot, serr := h.scan()
	if serr != nil {
		t.Fatalf("scan while authorized: %v", serr)
	}
	if snapshot.Content == nil || snapshot.Content.Contents["doomed.txt"] == nil {
		t.Fatalf("pre-revocation scan did not see the worktree: %+v", snapshot.Content)
	}

	// Protect the run: a non-owner collaborator may no longer steer it.
	e.run.Protected = true
	if err := e.store.UpdateRun(context.Background(), e.run); err != nil {
		t.Fatal(err)
	}

	// The already-connected client must lose the stream, and its writes
	// must stop landing.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, serr = h.scan(); serr != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stream still serving after authorization was revoked")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A write attempted after revocation cannot reach the worktree.
	_, _ = h.transition([]*core.Change{{
		Path: "doomed.txt",
		Old:  &core.Entry{Kind: core.EntryKind_File, Digest: []byte("whatever")},
	}})
	body, rerr := os.ReadFile(filepath.Join(worktree, "doomed.txt"))
	if rerr != nil || string(body) != "still here" {
		t.Fatalf("worktree file = %q (err %v), want untouched after revocation", body, rerr)
	}
}

// Removing the member revokes a live stream too, not only a protection
// flip: the same re-validation covers membership.
func TestSyncRevokesStreamWhenMemberIsRemoved(t *testing.T) {
	mutagenDataDir(t)
	e := newTestEnv(t, func(c *Config) { c.revalidateInterval = 50 * time.Millisecond })
	worktree := t.TempDir()
	e.run.Worktree = worktree
	e.run.Status = domain.RunNeedsAttention
	if err := e.store.UpdateRun(context.Background(), e.run); err != nil {
		t.Fatal(err)
	}
	collab, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	h := dialHostileSync(t, e, collab, e.run.ID)
	defer h.Close()
	if resp, err := h.init(hostileInit(worktree, "hostile", &synchronization.Configuration{}, false)); err != nil {
		t.Fatalf("init: %v", err)
	} else if resp.Error != "" {
		t.Fatalf("init refused: %s", resp.Error)
	}
	if _, serr := h.scan(); serr != nil {
		t.Fatalf("scan while authorized: %v", serr)
	}

	if err := e.store.DeleteMember(context.Background(), cm.ID); err != nil {
		t.Fatalf("delete member: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, serr := h.scan(); serr != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("removed member still being served")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Finding 6a: mutagen's generic decoder sizes its buffer from the length
// prefix alone, up to 100 MiB, before reading any of the message. The
// bridge must reject an oversized frame instead of allocating for it.
func TestSyncRejectsOversizedInitFrame(t *testing.T) {
	e, _ := syncEnv(t)
	h := dialHostileSync(t, e, e.signer, e.run.ID)
	defer h.Close()

	// A length prefix just past the cap, with no body behind it.
	var prefix [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(prefix[:], maxSyncInitBytes+1)
	if _, err := h.pipe.Write(prefix[:n]); err != nil {
		t.Fatalf("write length prefix: %v", err)
	}

	// The bridge must drop the channel rather than wait for a body that
	// will never arrive.
	assertSyncChannelClosed(t, h, 15*time.Second, "oversized init frame")
}

// The JSON header is bounded too: protocol.MaxLineBytes is 32 MiB for
// profile pushes, which would let one sync channel buffer that much
// before the run ID is even parsed.
func TestSyncRejectsOversizedHeader(t *testing.T) {
	e, _ := syncEnv(t)
	client, err := e.dialWith(e.signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	pipe := openSubsystem(t, client, protocol.SubsystemSync, nil)

	// A newline-free header far past the cap but far under MaxLineBytes.
	flood := append([]byte(`{"run_id":"`), []byte(strings.Repeat("a", 4*maxSyncHeaderBytes))...)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = pipe.Write(flood)
		_, _ = io.Copy(io.Discard, pipe)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("oversized header was accepted; the channel is still open")
	}
}

// Finding 6b: the post-auth SSH deadline is cleared, so without a
// handshake deadline an authenticated member can pin a channel (and its
// goroutine) forever by never finishing the setup.
func TestSyncBoundsSlowHandshake(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.syncHandshakeTimeout = 300 * time.Millisecond })
	worktree := t.TempDir()
	e.run.Worktree = worktree
	e.run.Status = domain.RunNeedsAttention
	if err := e.store.UpdateRun(context.Background(), e.run); err != nil {
		t.Fatal(err)
	}

	// Stall before the init frame: header sent, ack read, compression
	// byte exchanged, then nothing.
	h := dialHostileSync(t, e, e.signer, e.run.ID)
	defer h.Close()
	assertSyncChannelClosed(t, h, 15*time.Second, "stalled init frame")

	// Stalling before the header is bounded by the same deadline.
	client, err := e.dialWith(e.signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	pipe := openSubsystem(t, client, protocol.SubsystemSync, nil)
	if _, werr := pipe.Write([]byte(`{"run_id":"`)); werr != nil {
		t.Fatalf("write partial header: %v", werr)
	}
	done := make(chan error, 1)
	go func() {
		_, rerr := io.ReadAll(pipe)
		done <- rerr
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("partial header was not bounded by the handshake deadline")
	}
}

// A member cannot fan out unbounded concurrent sync channels: each one
// owns a mutagen endpoint with watcher and staging goroutines.
func TestSyncCapsConcurrentChannelsPerMember(t *testing.T) {
	e, _ := syncEnv(t)
	client, err := e.dialWith(e.signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// Hold the cap open. Each channel stays parked before its init frame,
	// which is enough to occupy a slot.
	for i := range maxSyncChannelsPerMember {
		pipe := openSubsystem(t, client, protocol.SubsystemSync, nil)
		header, _ := json.Marshal(protocol.SyncRequest{RunID: string(e.run.ID), Force: true})
		if _, werr := pipe.Write(append(header, '\n')); werr != nil {
			t.Fatalf("write header %d: %v", i, werr)
		}
		var ack protocol.SyncResponse
		readJSONLine(t, bufio.NewReader(pipe), &ack)
		if !ack.OK {
			t.Fatalf("ack %d = %+v, want ok below the cap", i, ack)
		}
	}
	if ack := syncAck(t, e, e.signer, e.run.ID, true); ack.OK || ack.Code != protocol.CodeConflict {
		t.Fatalf("ack over the cap = %+v, want conflict", ack)
	}
}

// Finding 7: the conflict payload is client-supplied and reaches every
// subscriber of the run's workspace, so it must be bounded to what a
// conflict report can be. Paths that escape the sync root, or carry
// terminal control sequences, are not conflict reports.
func TestSyncConflictRejectsHostilePayloads(t *testing.T) {
	e, _ := syncEnv(t)
	c := controlAs(t, e, e.signer)
	for _, tc := range []struct {
		name  string
		files []string
		sync  string
	}{
		{"absolute path", []string{"/etc/shadow"}, ""},
		{"parent traversal", []string{"../../etc/shadow"}, ""},
		{"dot segment", []string{"a/./b"}, ""},
		{"terminal escape", []string{"a\x1b]0;pwned\x07.txt"}, ""},
		{"newline injection", []string{"a\nb"}, ""},
		{"control chars in session id", []string{"a.txt"}, "sync\x1b[2J"},
		{"path flood", make([]string, maxConflictFiles+1), ""},
	} {
		files := tc.files
		if tc.name == "path flood" {
			for i := range files {
				files[i] = "f.txt"
			}
		}
		err := c.Call(protocol.MethodSyncConflict, protocol.SyncConflictParams{
			RunID: string(e.run.ID), SyncSessionID: tc.sync, Files: files,
		}, nil)
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
			t.Errorf("%s: err = %v, want invalid params", tc.name, err)
		}
	}

	// A genuine report still goes through.
	if err := c.Call(protocol.MethodSyncConflict, protocol.SyncConflictParams{
		RunID: string(e.run.ID), SyncSessionID: "sync_1",
		Files: []string{"src/main.go", "docs/readme.md" + overlay.ConflictSuffix},
	}, nil); err != nil {
		t.Fatalf("valid conflict report: %v", err)
	}
}

// Finding 7, delivery half: the typed sync.conflict event only reaches a
// client that asked for its type, and the one client known to be watching
// a synced run (`aether sync`) subscribes to run.status. Without a second
// notice on the workspace timeline the run owner never learns their
// worktree is half-synced.
func TestSyncConflictNotifiesAffectedMembersOnTimeline(t *testing.T) {
	e, _ := syncEnv(t)
	collab, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	// The run owner, watching the workspace the way every other privileged
	// act on someone else's run is surfaced.
	sub, err := e.bus.Subscribe(context.Background(), events.SubscribeOptions{
		Filter: events.Filter{Workspace: e.ws.ID, Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	cc := controlAs(t, e, collab)
	if err := cc.Call(protocol.MethodSyncConflict, protocol.SyncConflictParams{
		RunID: string(e.run.ID), SyncSessionID: "sync_1", Files: []string{"src/main.go"},
	}, nil); err != nil {
		t.Fatalf("sync.conflict: %v", err)
	}

	select {
	case ev := <-sub.Events():
		p, ok := ev.Payload.(events.TimelinePayload)
		if !ok {
			t.Fatalf("payload type %T", ev.Payload)
		}
		if ev.RunID != e.run.ID || ev.ActorID != cm.ID {
			t.Fatalf("envelope run/actor = %s/%s, want %s/%s", ev.RunID, ev.ActorID, e.run.ID, cm.ID)
		}
		if !strings.Contains(p.Message, "overlay paused") {
			t.Fatalf("timeline message = %q, want the paused-overlay notice", p.Message)
		}
		// The timeline is workspace-wide, so it carries the fact of the
		// conflict but not the conflicted paths.
		if strings.Contains(p.Message, "src/main.go") {
			t.Fatalf("timeline message leaks worktree paths: %q", p.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("affected members were never told about the conflict")
	}
}

// assertSyncChannelClosed waits for the server to drop the sync channel.
func assertSyncChannelClosed(t *testing.T, h *hostileSync, within time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, h.r)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("%s: channel still open, the server is waiting indefinitely", what)
	}
}
