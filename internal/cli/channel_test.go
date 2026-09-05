package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestSessionStreamSurfacesRemoteExitFailureAfterOutput(t *testing.T) {
	wantErr := errors.New("remote process exited with status 1")
	waitCalls := 0
	stream := &sessionStream{
		Reader: strings.NewReader("setup failed\n"),
		wait: func() error {
			waitCalls++
			return wantErr
		},
	}
	body, err := io.ReadAll(stream)
	if string(body) != "setup failed\n" {
		t.Fatalf("body = %q", body)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("read error = %v, want %v", err, wantErr)
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, wantErr) {
		t.Fatalf("second read error = %v, want %v", err, wantErr)
	}
	if waitCalls != 1 {
		t.Fatalf("wait calls = %d, want 1", waitCalls)
	}
}

func TestSessionStreamKeepsEOFAfterSuccessfulRemoteExit(t *testing.T) {
	waitCalls := 0
	stream := &sessionStream{
		Reader: strings.NewReader("setup complete\n"),
		wait: func() error {
			waitCalls++
			return nil
		},
	}
	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read error = %v, want nil", err)
	}
	if string(body) != "setup complete\n" {
		t.Fatalf("body = %q", body)
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("second read error = %v, want EOF", err)
	}
	if waitCalls != 1 {
		t.Fatalf("wait calls = %d, want 1", waitCalls)
	}
}

func TestSessionStreamCloseWriteLeavesRemoteOutputReadable(t *testing.T) {
	stdin := &trackingWriteCloser{}
	waitCalls := 0
	stream := &sessionStream{
		Reader: strings.NewReader("remote complete\n"),
		stdin:  stdin,
		wait: func() error {
			waitCalls++
			return nil
		},
	}

	if err := stream.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read error = %v, want nil", err)
	}
	if string(body) != "remote complete\n" {
		t.Fatalf("body = %q", body)
	}
	if stdin.closeCalls != 1 {
		t.Fatalf("stdin close calls = %d, want 1", stdin.closeCalls)
	}
	if waitCalls != 1 {
		t.Fatalf("wait calls = %d, want 1", waitCalls)
	}
}

type trackingWriteCloser struct {
	closeCalls int
}

func (w *trackingWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *trackingWriteCloser) Close() error {
	w.closeCalls++
	return nil
}
func TestAttachResponseKeepsAckErrorAndLeftoverBytes(t *testing.T) {
	stream := &sessionStream{
		Reader: strings.NewReader(`{"ok":false,"code":-32000,"error":"attach denied"}` + "\n" + "terminal output"),
		stdin:  &trackingWriteCloser{},
	}
	var ack protocol.AttachResponse
	out, err := readAck(stream, &ack)
	if err != nil {
		t.Fatalf("readAck: %v", err)
	}
	if ack.OK || ack.Code != -32000 || ack.Error != "attach denied" {
		t.Fatalf("ack = %+v", ack)
	}
	body, err := io.ReadAll(out)
	if err != nil {
		t.Fatalf("read leftover: %v", err)
	}
	if string(body) != "terminal output" {
		t.Fatalf("leftover = %q, want terminal output", body)
	}
}

func TestTerminalResponseKeepsAckErrorAndLeftoverBytes(t *testing.T) {
	stream := &sessionStream{
		Reader: strings.NewReader(`{"ok":true,"tab":"main","cols":80,"rows":24}` + "\n" + "terminal output"),
		stdin:  &trackingWriteCloser{},
	}
	var ack protocol.TerminalResponse
	out, err := readAck(stream, &ack)
	if err != nil {
		t.Fatalf("readAck: %v", err)
	}
	if !ack.OK || ack.Tab != "main" || ack.Cols != 80 || ack.Rows != 24 {
		t.Fatalf("ack = %+v", ack)
	}
	body, err := io.ReadAll(out)
	if err != nil {
		t.Fatalf("read leftover: %v", err)
	}
	if string(body) != "terminal output" {
		t.Fatalf("leftover = %q, want terminal output", body)
	}
}

func TestForwardOpensDirectTCPIPChannel(t *testing.T) {
	_, hostSigner, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostSigner)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	payloads := make(chan struct {
		host     string
		port     uint32
		origHost string
		origPort uint32
	}, 1)
	go func() {
		raw, aerr := listener.Accept()
		if aerr != nil {
			return
		}
		conn, chans, reqs, serr := ssh.NewServerConn(raw, serverConfig)
		if serr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		go ssh.DiscardRequests(reqs)
		for newCh := range chans {
			var payload struct {
				DestHost string
				DestPort uint32
				OrigHost string
				OrigPort uint32
			}
			_ = ssh.Unmarshal(newCh.ExtraData(), &payload)
			payloads <- struct {
				host     string
				port     uint32
				origHost string
				origPort uint32
			}{payload.DestHost, payload.DestPort, payload.OrigHost, payload.OrigPort}
			ch, requests, cerr := newCh.Accept()
			if cerr != nil {
				return
			}
			go ssh.DiscardRequests(requests)
			go func() {
				_, _ = io.Copy(ch, ch)
				_ = ch.Close()
			}()
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	stream, err := (&Conn{client: client}).Forward("run_1", 1455)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	defer func() { _ = stream.Close() }()
	select {
	case payload := <-payloads:
		if payload.host != "run:run_1" || payload.port != 1455 ||
			payload.origHost != "127.0.0.1" || payload.origPort != 0 {
			t.Fatalf("payload = %#v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not receive direct-tcpip payload")
	}
	if _, err := stream.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, len("hello"))
	if _, err := io.ReadFull(stream, body); err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" {
		t.Fatalf("echo = %q", body)
	}
}
