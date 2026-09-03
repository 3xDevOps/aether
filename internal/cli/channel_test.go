package cli

import (
	"errors"
	"io"
	"strings"
	"testing"

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
