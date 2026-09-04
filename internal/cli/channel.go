package cli

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// sessionStream is one SSH session channel's stdio as a byte stream.
// Close ends the channel without tearing down the parent connection.
//
// x/crypto's Session.Wait cannot report a subsystem's exit status:
// RequestSubsystem never marks the session started, so Wait fails with
// "ssh: session not started" no matter what the server sent. The stream
// therefore talks to the raw channel and collects the exit-status
// request itself.
type sessionStream struct {
	io.Reader
	stdin io.WriteCloser
	// ch is the raw session channel, kept for out-of-band channel
	// requests (window-change); nil in tests that fake the stream.
	ch       ssh.Channel
	closeCh  func() error
	wait     func() error
	waitOnce sync.Once
	waitErr  error
}

func (s *sessionStream) Read(p []byte) (int, error) {
	n, err := s.Reader.Read(p)
	if err != io.EOF {
		return n, err
	}
	s.waitOnce.Do(func() {
		if s.wait != nil {
			s.waitErr = s.wait()
		}
	})
	if s.waitErr != nil {
		return n, s.waitErr
	}
	return n, io.EOF
}

func (s *sessionStream) Write(p []byte) (int, error) { return s.stdin.Write(p) }
func (s *sessionStream) CloseWrite() error           { return s.stdin.Close() }
func (s *sessionStream) Close() error {
	_ = s.CloseWrite()
	if s.closeCh == nil {
		return nil
	}
	return s.closeCh()
}

// channelStdin adapts an SSH channel's write side to io.WriteCloser with
// Close as half-close, so ending input leaves remote output readable.
type channelStdin struct{ ch ssh.Channel }

func (w channelStdin) Write(p []byte) (int, error) { return w.ch.Write(p) }
func (w channelStdin) Close() error                { return w.ch.CloseWrite() }

// ptyGeometry is the terminal size requested for a subsystem channel.
type ptyGeometry struct{ cols, rows uint }

func (c *Conn) openSubsystem(name string, pty *ptyGeometry) (*sessionStream, error) {
	ch, reqs, err := c.client.OpenChannel("session", nil)
	if err != nil {
		return nil, fmt.Errorf("cli: open session: %w", err)
	}
	exit := make(chan error, 1)
	go func() { exit <- awaitExitStatus(reqs) }()
	if pty != nil {
		if perr := requestPTY(ch, pty.cols, pty.rows); perr != nil {
			_ = ch.Close()
			return nil, perr
		}
	}
	ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Subsystem string }{name}))
	if err == nil && !ok {
		err = errors.New("request refused")
	}
	if err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("cli: subsystem %s: %w", name, err)
	}
	return &sessionStream{
		Reader:  ch,
		ch:      ch,
		stdin:   channelStdin{ch: ch},
		closeCh: ch.Close,
		wait:    func() error { return <-exit },
	}, nil
}

// RemoteExitError is a subsystem channel ending with a nonzero exit
// status. The attach subsystem uses the protocol.AttachExit* statuses to
// say why the server dropped a live attach.
type RemoteExitError struct{ Status int }

func (e *RemoteExitError) Error() string {
	return fmt.Sprintf("cli: remote exited with status %d", e.Status)
}

// awaitExitStatus consumes session requests until the channel closes.
// Exit status 0, or a close without any status, is a clean end; a
// nonzero status carries the remote failure.
func awaitExitStatus(reqs <-chan *ssh.Request) error {
	var res error
	for req := range reqs {
		if req.Type == "exit-status" && len(req.Payload) >= 4 {
			if status := binary.BigEndian.Uint32(req.Payload); status != 0 {
				res = &RemoteExitError{Status: int(status)}
			}
		}
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
	}
	return res
}

// requestPTY mirrors the wire format of x/crypto Session.RequestPty with
// an empty mode list.
func requestPTY(ch ssh.Channel, cols, rows uint) error {
	req := struct {
		Term          string
		Cols, Rows    uint32
		Width, Height uint32
		Modes         string
	}{
		Term: "xterm-256color",
		Cols: uint32(cols), Rows: uint32(rows),
		Width: uint32(cols * 8), Height: uint32(rows * 8),
		Modes: string([]byte{0}),
	}
	ok, err := ch.SendRequest("pty-req", true, ssh.Marshal(&req))
	if err == nil && !ok {
		err = errors.New("cli: pty-req refused")
	}
	return err
}

// Terminal is an interactive remote terminal: a byte stream whose window
// can be resized while it is open.
type Terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows uint) error
}

// TerminalStream is a PTY-backed subsystem stream; Resize sends the RFC
// 4254 window-change request on the underlying session channel.
type TerminalStream struct {
	*bufferedStream
}

var _ Terminal = (*TerminalStream)(nil)

// Resize adjusts the remote PTY to cols by rows.
func (t *TerminalStream) Resize(cols, rows uint) error {
	payload := ssh.Marshal(struct {
		Cols, Rows, WidthPx, HeightPx uint32
	}{Cols: uint32(cols), Rows: uint32(rows)})
	if _, err := t.ch.SendRequest("window-change", false, payload); err != nil {
		return fmt.Errorf("cli: window-change: %w", err)
	}
	return nil
}

// Control opens the JSON-RPC control channel.
func (c *Conn) Control() (*protocol.Client, error) {
	stream, err := c.openSubsystem(protocol.SubsystemControl, nil)
	if err != nil {
		return nil, err
	}
	return protocol.NewClient(stream), nil
}

// AttachStream opens the attach subsystem for req and returns the
// resizable terminal stream alongside the server's ack. A refused ack is
// returned with the error so callers can forward its code.
func (c *Conn) AttachStream(req protocol.AttachRequest) (*TerminalStream, protocol.AttachResponse, error) {
	var ack protocol.AttachResponse
	out, err := c.openStream(protocol.SubsystemAttach, &ptyGeometry{cols: req.Cols, rows: req.Rows}, req, "attach", &ack)
	if err != nil {
		return nil, ack, err
	}
	if !ack.OK {
		_ = out.Close()
		return nil, ack, fmt.Errorf("cli: attach: %s", ack.Error)
	}
	return &TerminalStream{bufferedStream: out}, ack, nil
}

// Attach opens the attach subsystem for runID with the given geometry and
// returns the raw PTY stream after a successful ack.
func (c *Conn) Attach(runID string, cols, rows uint) (io.ReadWriteCloser, error) {
	stream, _, err := c.AttachStream(protocol.AttachRequest{RunID: runID, Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// Sync opens the sync subsystem for runID and returns the raw mutagen
// endpoint stream after a successful ack. force overrides the server's
// mid-write refusal for runs that are currently running.
func (c *Conn) Sync(runID string, force bool) (io.ReadWriteCloser, error) {
	var ack protocol.SyncResponse
	out, err := c.openStream(protocol.SubsystemSync, nil, protocol.SyncRequest{RunID: runID, Force: force}, "sync", &ack)
	if err != nil {
		return nil, err
	}
	if !ack.OK {
		_ = out.Close()
		return nil, fmt.Errorf("cli: sync: %s", ack.Error)
	}
	return out, nil
}

// EventsStream opens the events subsystem with the given subscription and
// returns the raw NDJSON event stream after a successful ack. A refused
// subscription comes back as *protocol.Error with the server's code.
func (c *Conn) EventsStream(req protocol.SubscribeRequest) (io.ReadWriteCloser, error) {
	var ack protocol.SubscribeResponse
	out, err := c.openStream(protocol.SubsystemEvents, nil, req, "subscribe", &ack)
	if err != nil {
		return nil, err
	}
	if !ack.OK {
		_ = out.Close()
		return nil, &protocol.Error{Code: ack.Code, Message: ack.Error}
	}
	return out, nil
}

// Events opens the events subsystem with the given subscription and
// returns the raw NDJSON event stream after a successful ack.
func (c *Conn) Events(req protocol.SubscribeRequest) (io.ReadWriteCloser, error) {
	out, err := c.EventsStream(req)
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return nil, fmt.Errorf("cli: subscribe: %s", perr.Message)
	}
	return out, err
}

// bufferedStream keeps leftover bytes from the ack-line bufio.Reader so
// they are not lost to the raw PTY/setup stream.
type bufferedStream struct {
	r *bufio.Reader
	*sessionStream
}

func (s *bufferedStream) Read(p []byte) (int, error) { return s.r.Read(p) }

func readAck(stream *sessionStream, v any) (*bufferedStream, error) {
	br := bufio.NewReader(stream)
	line, err := protocol.ReadLine(br)
	if err != nil {
		return nil, fmt.Errorf("cli: read ack: %w", err)
	}
	if err := json.Unmarshal(line, v); err != nil {
		return nil, fmt.Errorf("cli: decode ack: %w", err)
	}
	return &bufferedStream{r: br, sessionStream: stream}, nil
}

// openStream opens a subsystem, writes its one-line JSON header, and reads
// the ack line into ack; the returned stream carries any bytes past the
// ack. Refusal is the caller's to detect: the ack is decoded either way.
func (c *Conn) openStream(name string, pty *ptyGeometry, header any, what string, ack any) (*bufferedStream, error) {
	stream, err := c.openSubsystem(name, pty)
	if err != nil {
		return nil, err
	}
	line, err := json.Marshal(header)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if _, err = stream.Write(append(line, '\n')); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("cli: write %s header: %w", what, err)
	}
	out, err := readAck(stream, ack)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	return out, nil
}

// GitURL is the ssh:// remote matching sshd's git-upload-pack / receive-pack
// path (workspace ID, optional .git suffix, leading slash).
func GitURL(user, addr, workspaceID string) string {
	if user == "" {
		user = "aether"
	}
	return "ssh://" + user + "@" + addr + "/" + workspaceID + ".git"
}
