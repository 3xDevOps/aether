package localops

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestForwardManagerForwardsConnectionsAndTracksStatus(t *testing.T) {
	port := freeForwardPort(t)
	manager := NewForwardManager()
	dial := func() (io.ReadWriteCloser, error) {
		client, server := net.Pipe()
		go func() {
			_, _ = io.Copy(server, server)
			_ = server.Close()
		}()
		return client, nil
	}
	if err := manager.Start("run-1", port, dial); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := manager.Start("run-1", port, dial); err == nil {
		t.Fatal("duplicate Start succeeded")
	}

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len("hello"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("echo = %q", buf)
	}

	deadline := time.Now().Add(time.Second)
	for {
		status := manager.Status()
		if len(status) == 1 && status[0].RunID == "run-1" && status[0].Port == port && status[0].LocalPort == port && status[0].Conns == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.Stop("run-1", port); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := manager.Status(); len(got) != 0 {
		t.Fatalf("status after stop = %#v", got)
	}
}

func freeForwardPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}
