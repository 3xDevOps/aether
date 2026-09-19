package coordtransport

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestCallAllowsMaximumInboxWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coord3.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"messages":[]}}` + "\n"))
	}()

	var result protocol.CoordInboxResult
	if err := Call(context.Background(), path, protocol.MethodCoordInbox,
		protocol.CoordInboxParams{WaitSeconds: protocol.CoordMaxInboxWaitSeconds}, &result); err != nil {
		t.Fatalf("maximum inbox wait: %v", err)
	}
	if result.Messages == nil || len(result.Messages) != 0 {
		t.Fatalf("result = %+v, want a valid empty inbox", result)
	}
}
