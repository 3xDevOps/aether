package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// sessionStream is one SSH session's stdio as a byte stream. Close ends
// the session without tearing down the parent connection.
type sessionStream struct {
	io.Reader
	stdin    io.WriteCloser
	sess     *ssh.Session
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
	return s.sess.Close()
}

// Resize forwards a terminal window change to the remote session.
func (s *sessionStream) Resize(cols, rows uint) error {
	return s.sess.WindowChange(int(rows), int(cols))
}

func (c *Conn) openSubsystem(name string, pty func(*ssh.Session) error) (*sessionStream, error) {
	sess, err := c.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("cli: open session: %w", err)
	}
	if pty != nil {
		if err = pty(sess); err != nil {
			_ = sess.Close()
			return nil, err
		}
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("cli: stdin pipe: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("cli: stdout pipe: %w", err)
	}
	if err := sess.RequestSubsystem(name); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("cli: subsystem %s: %w", name, err)
	}
	return &sessionStream{Reader: stdout, stdin: stdin, sess: sess, wait: sess.Wait}, nil
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
	stream, err := c.openSubsystem(protocol.SubsystemAttach, func(sess *ssh.Session) error {
		return sess.RequestPty("xterm-256color", int(rows), int(cols), ssh.TerminalModes{})
	})
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

// WorkspaceShell opens the unified workspace-shell subsystem for bootstrap
// tools or harness login and returns the raw terminal stream after its ack.
func (c *Conn) WorkspaceShell(req protocol.WorkspaceShellRequest) (io.ReadWriteCloser, error) {
	stream, err := c.openSubsystem(protocol.SubsystemWorkspaceShell, func(sess *ssh.Session) error {
		return sess.RequestPty("xterm-256color", int(req.Rows), int(req.Cols), ssh.TerminalModes{})
	})
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
		return nil, fmt.Errorf("cli: write workspace shell header: %w", err)
	}
	var ack protocol.WorkspaceShellResponse
	out, err := readAck(stream, &ack)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if !ack.OK {
		_ = stream.Close()
		return nil, workspaceShellAckError(ack)
	}
	return out, nil
}

func workspaceShellAckError(ack protocol.WorkspaceShellResponse) error {
	if ack.Code != 0 {
		return fmt.Errorf("cli: workspace shell: %s (code %d)", ack.Error, ack.Code)
	}
	return fmt.Errorf("cli: workspace shell: %s", ack.Error)
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
