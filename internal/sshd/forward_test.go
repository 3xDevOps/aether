package sshd

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
)

type forwardTestPayload struct {
	DestHost string
	DestPort uint32
	OrigHost string
	OrigPort uint32
}

func TestDirectTCPIPOwnerEchoAndHalfClose(t *testing.T) {
	e := newTestEnv(t, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	e.runs.setAddr("127.0.0.1")

	serverDone := make(chan error, 1)
	go func() {
		conn, aerr := listener.Accept()
		if aerr != nil {
			serverDone <- aerr
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 1024)
		for {
			n, rerr := conn.Read(buf)
			if n > 0 {
				if _, werr := conn.Write(buf[:n]); werr != nil {
					serverDone <- werr
					return
				}
			}
			if errors.Is(rerr, io.EOF) {
				_, rerr = conn.Write([]byte("done"))
				serverDone <- rerr
				return
			}
			if rerr != nil {
				serverDone <- rerr
				return
			}
		}
	}()

	client := e.dial(t)
	ch, reqs, err := client.OpenChannel("direct-tcpip", ssh.Marshal(forwardTestPayload{
		DestHost: "run:" + string(e.run.ID),
		DestPort: uint32(listener.Addr().(*net.TCPAddr).Port),
		OrigHost: "127.0.0.1",
	}))
	if err != nil {
		t.Fatalf("OpenChannel: %v", err)
	}
	go func() {
		for range reqs {
		}
	}()
	defer func() { _ = ch.Close() }()

	if _, err := ch.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readForwardBytes(t, ch, 5); string(got) != "hello" {
		t.Fatalf("echo = %q, want hello", got)
	}
	if err := ch.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	if got := readForwardBytes(t, ch, 4); string(got) != "done" {
		t.Fatalf("half-close response = %q, want done", got)
	}
	readForwardEOF(t, ch)
	if err := <-serverDone; err != nil {
		t.Fatalf("echo server: %v", err)
	}
}

func TestDirectTCPIPRejectsUnknownRun(t *testing.T) {
	e := newTestEnv(t, nil)
	client := e.dial(t)
	assertForwardRejected(t, client, forwardTestPayload{DestHost: "run:missing", DestPort: 1}, "run not found")
}

func TestDirectTCPIPRejectsProtectedRunCollaborator(t *testing.T) {
	e := newTestEnv(t, nil)
	if err := e.store.SetRunProtected(t.Context(), e.run.ID, true); err != nil {
		t.Fatalf("protect run: %v", err)
	}
	signer, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	client, err := e.dialWith(signer, nil)
	if err != nil {
		t.Fatalf("dial collaborator: %v", err)
	}
	defer func() { _ = client.Close() }()
	assertForwardRejected(t, client, forwardTestPayload{DestHost: "run:" + string(e.run.ID), DestPort: 1}, "permission denied: run is protected: only its owner or an admin may steer")
}

func TestDirectTCPIPRejectsNonRunDestination(t *testing.T) {
	e := newTestEnv(t, nil)
	assertForwardRejected(t, e.dial(t), forwardTestPayload{DestHost: "127.0.0.1", DestPort: 1}, "port forwarding targets must be run:<run-id>")
}

func TestDirectTCPIPRejectsViewer(t *testing.T) {
	e := newTestEnv(t, nil)
	signer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)
	client, err := e.dialWith(signer, nil)
	if err != nil {
		t.Fatalf("dial viewer: %v", err)
	}
	defer func() { _ = client.Close() }()
	assertForwardRejected(t, client, forwardTestPayload{DestHost: "run:" + string(e.run.ID), DestPort: 1}, "permission denied: steer requires the collaborator role")
}

func assertForwardRejected(t *testing.T, client *ssh.Client, payload forwardTestPayload, message string) {
	t.Helper()
	_, _, err := client.OpenChannel("direct-tcpip", ssh.Marshal(payload))
	if err == nil {
		t.Fatal("OpenChannel succeeded, want rejection")
	}
	var openErr *ssh.OpenChannelError
	if !errors.As(err, &openErr) {
		t.Fatalf("OpenChannel error = %T %v, want *ssh.OpenChannelError", err, err)
	}
	if openErr.Reason != ssh.Prohibited {
		t.Errorf("rejection reason = %v, want ssh.Prohibited", openErr.Reason)
	}
	if openErr.Message != message {
		t.Errorf("rejection message = %q, want %q", openErr.Message, message)
	}
}

func readForwardBytes(t *testing.T, ch ssh.Channel, n int) []byte {
	t.Helper()
	result := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data := make([]byte, n)
		_, err := io.ReadFull(ch, data)
		result <- struct {
			data []byte
			err  error
		}{data: data, err: err}
	}()
	select {
	case result := <-result:
		if result.err != nil {
			t.Fatalf("read %d bytes: %v", n, result.err)
		}
		return result.data
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out reading %d bytes", n)
		return nil
	}
}

func readForwardEOF(t *testing.T, ch ssh.Channel) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := ch.Read(buf)
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("read after half-close = %v, want EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forwarding EOF")
	}
}
