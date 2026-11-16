package helper

import (
	"bufio"
	"io"
	"net"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/guest"
)

func TestALoopbackPortReachesItsSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "server.sock")
	server, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	go func() {
		for {
			conn, acceptErr := server.Accept()
			if acceptErr != nil {
				return
			}
			go func() { _, _ = io.Copy(conn, conn); _ = conn.Close() }()
		}
	}()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	if err = listenLoopbacks([]guest.Loopback{{Port: uint16(port), Path: sock}}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", probe.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.Write([]byte("GET /v1/healthz\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "GET /v1/healthz\n" {
		t.Fatalf("relayed %q, %v", line, err)
	}
}
