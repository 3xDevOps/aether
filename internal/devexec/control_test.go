package devexec

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func finalRecord(t *testing.T, key string) State {
	t.Helper()
	dir := stateDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	code := 143
	return State{CreationKey: key, ExecID: "exec-1", ClaimToken: "claim-1", Exited: true, ExitCode: &code}
}

// A supervisor that exits writes its final record, then closes the calls it
// accepted unanswered, whether or not it read their request; such a call
// reads the record.
func TestControlReadsTheFinalRecordOfASupervisorThatExitsMidCall(t *testing.T) {
	for name, readsRequest := range map[string]bool{"after reading the request": true, "before reading the request": false} {
		t.Run(name, func(t *testing.T) {
			key := "control-test-" + t.Name() + "-" + rand.Text()
			final := finalRecord(t, key)
			listener, err := net.Listen("unix", filepath.Join(stateDir(key), "control.sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				if readsRequest {
					_ = json.NewDecoder(io.LimitReader(conn, 16<<10)).Decode(&Request{})
				}
				if saveState(final, -1) == nil {
					_ = conn.Close()
				}
			}()
			got, err := Control(t.Context(), key, Request{Action: "status", ExecID: "exec-1", ClaimToken: "claim-1"})
			if err != nil || !got.Exited || got.ExitCode == nil || *got.ExitCode != 143 {
				t.Fatalf("status = %+v, %v; want the final record's exit 143", got, err)
			}
		})
	}
}

// A status call that finds the supervisor's socket gone reads the final
// record written before it went.
func TestControlFailureFallsBackToTheFinalRecord(t *testing.T) {
	key := "control-test-" + t.Name() + "-" + rand.Text()
	final := finalRecord(t, key)
	request := Request{Action: "status", ExecID: "exec-1", ClaimToken: "claim-1"}
	dialErr := &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}
	if _, err := orFinal(key, request, dialErr); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("without a final record = %v, want the dial error", err)
	}
	if err := saveState(final, -1); err != nil {
		t.Fatal(err)
	}
	if got, err := orFinal(key, request, dialErr); err != nil || got.ExitCode == nil || *got.ExitCode != 143 {
		t.Fatalf("with a final record = %+v, %v; want its exit 143", got, err)
	}
	if _, err := orFinal(key, Request{Action: "status", ExecID: "exec-2", ClaimToken: "claim-1"}, dialErr); err == nil || err.Error() != "execution identity mismatch" {
		t.Fatalf("another execution's record = %v, want an identity mismatch", err)
	}
	if _, err := orFinal(key, Request{Action: "start", ExecID: "exec-1", ClaimToken: "claim-1"}, dialErr); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("a failed start = %v, want its own error", err)
	}
}
