package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// sessionStream uses the raw channel because x/crypto's Session.Wait fails
// with "ssh: session not started" after RequestSubsystem, so it cannot
// report a subsystem's exit status.
type sessionStream struct {
	io.Reader
	stdin io.WriteCloser
	// nil in tests that fake the stream.
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

type ptyGeometry struct{ cols, rows uint }

func (c *Conn) openSubsystem(name string, pty *ptyGeometry) (*sessionStream, error) {
	ch, reqs, err := c.client.OpenChannel("session", nil)
	if err != nil {
		return nil, fmt.Errorf("cli: open session: %w", err)
	}
	exit := make(chan error, 1)
	go func() { exit <- awaitRequests(reqs) }()
	if pty != nil {
		if perr := requestPTY(ch, pty.cols, pty.rows); perr != nil {
			_ = ch.Close()
			return nil, perr
		}
	}
	ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Subsystem string }{name}))
	if err == nil && !ok {
		err = errSubsystemRefused
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

type RemoteExitError = protocol.RemoteExitError

// awaitRequests treats a close without any exit status as clean. Requests
// are acknowledged negatively so an accidental one cannot block closure.
func awaitRequests(reqs <-chan *ssh.Request) error {
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

func (t *TerminalStream) Resize(cols, rows uint) error {
	payload := ssh.Marshal(struct {
		Cols, Rows, WidthPx, HeightPx uint32
	}{Cols: uint32(cols), Rows: uint32(rows)})
	if _, err := t.ch.SendRequest("window-change", false, payload); err != nil {
		return fmt.Errorf("cli: window-change: %w", err)
	}
	return nil
}

func (c *Conn) Control() (*protocol.Client, error) {
	stream, err := c.openSubsystem(protocol.SubsystemControl, nil)
	if err != nil {
		return nil, err
	}
	return protocol.NewClient(stream), nil
}

// NewControlSessionID returns an opaque identifier stable for one logical
// attach. Callers retain it across reconnects and generate a new one for a
// separate tab.
func NewControlSessionID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		binary.BigEndian.PutUint64(raw[:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(raw[8:], uint64(time.Now().UnixNano())^uint64(len(raw)))
	}
	return hex.EncodeToString(raw[:])
}

// AttachStream returns a refused ack with the error so callers can forward
// its code.
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

// ACPStream opens an enhanced run's session stream. A refused ack comes
// back as *protocol.Error with the server's code.
func (c *Conn) ACPStream(req protocol.ACPStreamRequest) (io.ReadWriteCloser, protocol.ACPStreamResponse, error) {
	var ack protocol.ACPStreamResponse
	out, err := c.openStream(protocol.SubsystemACP, nil, req, "acp", &ack)
	if err != nil {
		return nil, ack, err
	}
	if !ack.OK {
		_ = out.Close()
		return nil, ack, &protocol.Error{Code: ack.Code, Message: ack.Error}
	}
	return out, ack, nil
}

// TerminalStream sends the geometry as an SSH pty-req before the JSON
// header, matching AttachStream.
func (c *Conn) TerminalStream(req protocol.TerminalRequest) (*TerminalStream, protocol.TerminalResponse, error) {
	var ack protocol.TerminalResponse
	out, err := c.openStream(protocol.SubsystemTerminal, &ptyGeometry{cols: req.Cols, rows: req.Rows}, req, "terminal", &ack)
	if err != nil {
		return nil, ack, err
	}
	if !ack.OK {
		_ = out.Close()
		return nil, ack, fmt.Errorf("cli: terminal: %s", ack.Error)
	}
	return &TerminalStream{bufferedStream: out}, ack, nil
}

func (c *Conn) Attach(runID string, cols, rows uint) (io.ReadWriteCloser, error) {
	stream, _, err := c.AttachStream(protocol.AttachRequest{
		RunID: runID, Cols: cols, Rows: rows, ControlSessionID: NewControlSessionID(),
	})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// Sync returns the raw mutagen endpoint stream. force overrides the
// server's mid-write refusal for running runs.
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

// EventsStream returns a refused subscription as *protocol.Error with the
// server's code.
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

// openStream decodes the ack whether or not it is a refusal; detecting
// refusal is the caller's job.
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
