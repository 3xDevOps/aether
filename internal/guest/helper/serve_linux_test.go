package helper

import (
	"bufio"
	"io"
	"net"
	"path/filepath"
	"testing"
)

func TestServedSocketSignalsOnlyOnceTheSocketAccepts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.sock")
	host, helperEnd := net.Pipe()
	go relayIn(helperEnd, path)
	if b, err := io.ReadAll(host); err != nil || len(b) != 0 {
		t.Fatalf("a socket that is not there yet sent %q, %v; want the connection closed", b, err)
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		c, acceptErr := l.Accept()
		if acceptErr != nil {
			return
		}
		line, _ := bufio.NewReader(c).ReadString('\n')
		_, _ = c.Write([]byte("got " + line))
		_ = c.Close()
	}()
	host, helperEnd = net.Pipe()
	go relayIn(helperEnd, path)
	r := bufio.NewReader(host)
	if b, readErr := r.ReadByte(); readErr != nil || b != 0 {
		t.Fatalf("first byte = %d, %v; want 0 once the socket accepted", b, readErr)
	}
	if _, err = host.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if line, readErr := r.ReadString('\n'); readErr != nil || line != "got hello\n" {
		t.Fatalf("relayed %q, %v", line, readErr)
	}
}
