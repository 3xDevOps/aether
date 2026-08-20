package cli

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
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
	stdin    io.WriteCloser
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
		stdin:   channelStdin{ch: ch},
		closeCh: ch.Close,
		wait:    func() error { return <-exit },
	}, nil
}

// awaitExitStatus consumes session requests until the channel closes.
// Exit status 0, or a close without any status, is a clean end; a
// nonzero status carries the remote failure.
func awaitExitStatus(reqs <-chan *ssh.Request) error {
	var res error
	for req := range reqs {
		if req.Type == "exit-status" && len(req.Payload) >= 4 {
			if status := binary.BigEndian.Uint32(req.Payload); status != 0 {
				res = fmt.Errorf("cli: remote exited with status %d", status)
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

// Control opens the JSON-RPC control channel.
func (c *Conn) Control() (*protocol.Client, error) {
	stream, err := c.openSubsystem(protocol.SubsystemControl, nil)
	if err != nil {
		return nil, err
	}
	return protocol.NewClient(stream), nil
}

// Attach opens the attach subsystem for runID with the given geometry and
// returns the raw PTY stream after a successful ack.
func (c *Conn) Attach(runID string, cols, rows uint) (io.ReadWriteCloser, error) {
	stream, err := c.openSubsystem(protocol.SubsystemAttach, &ptyGeometry{cols: cols, rows: rows})
	if err != nil {
		return nil, err
	}
	header, err := json.Marshal(protocol.AttachRequest{RunID: runID, Cols: cols, Rows: rows})
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if _, err = stream.Write(append(header, '\n')); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("cli: write attach header: %w", err)
	}
	var ack protocol.AttachResponse
	out, err := readAck(stream, &ack)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if !ack.OK {
		_ = stream.Close()
		return nil, fmt.Errorf("cli: attach: %s", ack.Error)
	}
	return out, nil
}

// Sync opens the sync subsystem for runID and returns the raw mutagen
// endpoint stream after a successful ack. force overrides the server's
// mid-write refusal for runs that are currently running.
func (c *Conn) Sync(runID string, force bool) (io.ReadWriteCloser, error) {
	stream, err := c.openSubsystem(protocol.SubsystemSync, nil)
	if err != nil {
		return nil, err
	}
	header, err := json.Marshal(protocol.SyncRequest{RunID: runID, Force: force})
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if _, err = stream.Write(append(header, '\n')); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("cli: write sync header: %w", err)
	}
	var ack protocol.SyncResponse
	out, err := readAck(stream, &ack)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if !ack.OK {
		_ = stream.Close()
		return nil, fmt.Errorf("cli: sync: %s", ack.Error)
	}
	return out, nil
}

// Events opens the events subsystem with the given subscription and
// returns the raw NDJSON event stream after a successful ack.
func (c *Conn) Events(req protocol.SubscribeRequest) (io.ReadWriteCloser, error) {
	stream, err := c.openSubsystem(protocol.SubsystemEvents, nil)
	if err != nil {
		return nil, err
	}
	header, err := json.Marshal(req)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if _, err = stream.Write(append(header, '\n')); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("cli: write subscribe: %w", err)
	}
	var ack protocol.SubscribeResponse
	out, err := readAck(stream, &ack)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if !ack.OK {
		_ = stream.Close()
		return nil, fmt.Errorf("cli: subscribe: %s", ack.Error)
	}
	return out, nil
}

// Setup opens the setup subsystem for harness (optional image) and
// returns the raw login stream after a successful ack.
func (c *Conn) Setup(harness, image string, cols, rows uint) (io.ReadWriteCloser, error) {
	stream, err := c.openSubsystem(protocol.SubsystemSetup, &ptyGeometry{cols: cols, rows: rows})
	if err != nil {
		return nil, err
	}
	header, err := json.Marshal(protocol.SetupRequest{Harness: harness, Image: image, Cols: cols, Rows: rows})
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if _, err = stream.Write(append(header, '\n')); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("cli: write setup header: %w", err)
	}
	var ack protocol.SetupResponse
	out, err := readAck(stream, &ack)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if !ack.OK {
		_ = stream.Close()
		return nil, fmt.Errorf("cli: setup: %s", ack.Error)
	}
	return out, nil
}

// ListenLocalForward listens on 127.0.0.1:localPort (0 picks an ephemeral
// port) and forwards accepted connections to 127.0.0.1:destPort through
// the SSH connection. The returned listener's Addr is the bound local address.
func (c *Conn) ListenLocalForward(localPort, destPort int) (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)))
	if err != nil {
		return nil, fmt.Errorf("cli: local listen: %w", err)
	}
	go func() {
		dst := net.JoinHostPort("127.0.0.1", strconv.Itoa(destPort))
		for {
			local, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = local.Close() }()
				remote, err := c.client.Dial("tcp", dst)
				if err != nil {
					return
				}
				defer func() { _ = remote.Close() }()
				done := make(chan struct{}, 2)
				go func() {
					_, _ = io.Copy(remote, local)
					done <- struct{}{}
				}()
				go func() {
					_, _ = io.Copy(local, remote)
					done <- struct{}{}
				}()
				<-done
			}()
		}
	}()
	return ln, nil
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

// GitURL is the ssh:// remote matching sshd's git-upload-pack / receive-pack
// path (workspace ID, optional .git suffix, leading slash).
func GitURL(user, addr, workspaceID string) string {
	if user == "" {
		user = "aether"
	}
	return "ssh://" + user + "@" + addr + "/" + workspaceID + ".git"
}
