package devexec

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// A supervisor that exits closes the calls it accepted unanswered, after it
// wrote its final record; such a call reads the record.
func TestControlReadsTheFinalRecordOfASupervisorThatExitsMidCall(t *testing.T) {
	key := "control-test-" + t.Name()
	dir := stateDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := net.Listen("unix", filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	code := 143
	final := State{CreationKey: key, ExecID: "exec-1", ClaimToken: "claim-1", Exited: true, ExitCode: &code}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = json.NewDecoder(io.LimitReader(conn, 16<<10)).Decode(&Request{})
		if saveState(final, -1) == nil {
			_ = conn.Close()
		}
	}()
	got, err := Control(t.Context(), key, Request{Action: "status", ExecID: "exec-1", ClaimToken: "claim-1"})
	if err != nil || !got.Exited || got.ExitCode == nil || *got.ExitCode != 143 {
		t.Fatalf("status = %+v, %v; want the final record's exit 143", got, err)
	}
}
