package cli

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeChannel is an ssh.Channel that records out-of-band requests, so the
// window-change wire format can be asserted without a live connection.
type fakeChannel struct {
	requests []recordedRequest
}

type recordedRequest struct {
	name      string
	wantReply bool
	payload   []byte
}

func (c *fakeChannel) Read(p []byte) (int, error)  { return 0, io.EOF }
func (c *fakeChannel) Write(p []byte) (int, error) { return len(p), nil }
func (c *fakeChannel) Close() error                { return nil }
func (c *fakeChannel) CloseWrite() error           { return nil }
func (c *fakeChannel) Stderr() io.ReadWriter       { return nil }

func (c *fakeChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	c.requests = append(c.requests, recordedRequest{name: name, wantReply: wantReply, payload: payload})
	return true, nil
}

var _ ssh.Channel = (*fakeChannel)(nil)

func TestTerminalStreamResizeSendsWindowChange(t *testing.T) {
	ch := &fakeChannel{}
	stream := &TerminalStream{bufferedStream: &bufferedStream{
		r:             bufio.NewReader(strings.NewReader("")),
		sessionStream: &sessionStream{Reader: strings.NewReader(""), stdin: &trackingWriteCloser{}, ch: ch},
	}}

	if err := stream.Resize(121, 33); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if len(ch.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(ch.requests))
	}
	req := ch.requests[0]
	if req.name != "window-change" {
		t.Fatalf("request name = %q, want window-change", req.name)
	}
	if req.wantReply {
		t.Fatal("window-change must not want a reply")
	}
	// RFC 4254 window-change: uint32 cols, rows, width_px, height_px.
	want := []byte{
		0, 0, 0, 121,
		0, 0, 0, 33,
		0, 0, 0, 0,
		0, 0, 0, 0,
	}
	if !bytes.Equal(req.payload, want) {
		t.Fatalf("payload = %v, want %v", req.payload, want)
	}
}
