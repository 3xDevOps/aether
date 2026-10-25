package coord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/coordcli"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type closedStdout struct{}

func (closedStdout) Write([]byte) (int, error) { return 0, errors.New("stdout is closed") }

func TestCLIInboxReadAcknowledgesThePreviousBatch(t *testing.T) {
	h := newHarness(t, 2)
	h.start()
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	t.Setenv(coordtransport.RunIDEnv, string(a))
	dir, err := h.svc.Provision(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, coordtransport.SocketName)
	t.Setenv("TMPDIR", t.TempDir())
	send := func(body string) {
		t.Helper()
		if _, rpcErr := h.svc.Send(ctx, b, sendParams(a, body)); rpcErr != nil {
			t.Fatal(rpcErr)
		}
	}
	cli := func(result any, args ...string) {
		t.Helper()
		var out bytes.Buffer
		code, err := coordcli.Run(ctx, args, coordcli.Config{Socket: socket, Out: &out, ErrOut: io.Discard})
		if err != nil || code != coordcli.ExitOK {
			t.Fatalf("%v: exit %d, error %v, output %s", args, code, err, out.String())
		}
		envelope := struct {
			Result any `json:"result"`
		}{Result: result}
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %v output %s: %v", args, out.String(), err)
		}
	}
	read := func() protocol.CoordInboxResult {
		t.Helper()
		var batch protocol.CoordInboxResult
		cli(&batch, "inbox")
		return batch
	}
	unacked := func() int {
		t.Helper()
		n, err := h.db.CountUnackedRunMessages(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	send("one")
	// A closed stdout stands in for a CLI that dies before the agent sees the batch.
	if code, err := coordcli.Run(ctx, []string{"inbox"}, coordcli.Config{Socket: socket, Out: closedStdout{}, ErrOut: io.Discard}); code != coordcli.ExitFailure || err == nil {
		t.Fatalf("inbox into a closed stdout: exit %d, error %v", code, err)
	}
	first := read()
	if len(first.Messages) != 1 || first.Messages[0].Body != "one" || first.Acked {
		t.Fatalf("read after a lost batch = %+v, want message one again with nothing acknowledged", first)
	}

	send("two")
	second := read()
	if len(second.Messages) != 1 || second.Messages[0].Body != "two" || !second.Acked || unacked() != 1 {
		t.Fatalf("second read = %+v with %d unacknowledged, want message two after acknowledging one", second, unacked())
	}

	send("three")
	var acked struct {
		Acked   string `json:"acked"`
		Waiting int    `json:"waiting"`
	}
	cli(&acked, "ack", second.AckToken)
	if acked.Acked != second.AckToken || acked.Waiting != 1 {
		t.Fatalf("explicit ack = %+v, want %s acknowledged with one message waiting", acked, second.AckToken)
	}
	third := read()
	if len(third.Messages) != 1 || third.Messages[0].Body != "three" || third.Acked || unacked() != 1 {
		t.Fatalf("read after an explicit ack = %+v with %d unacknowledged, want message three and nothing acknowledged twice", third, unacked())
	}
}
