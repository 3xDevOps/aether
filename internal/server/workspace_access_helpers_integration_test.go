//go:build integration

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// wsaMember is one member of the access matrix and the connection they
// act over.
type wsaMember struct {
	name    string
	id      string
	key     ssh.Signer
	keyPath string
	client  *ssh.Client
	ctrl    *protocol.Client
}

// outcome is what a call answered: the refusal's code and message, or the
// result. Two outcomes compare equal only when a client could not tell the
// calls apart.
type outcome struct {
	Code    int
	Message string
	Result  string
}

func (o outcome) refused() bool { return o.Code != 0 || o.Message != "" }

func outcomeOf(err error, result json.RawMessage) outcome {
	var pe *protocol.Error
	switch {
	case errors.As(err, &pe):
		return outcome{Code: pe.Code, Message: pe.Message}
	case err != nil:
		return outcome{Message: "transport: " + err.Error()}
	}
	return outcome{Result: string(result)}
}

func call(c *protocol.Client, method string, params any) outcome {
	var raw json.RawMessage
	return outcomeOf(c.Call(method, params, &raw), raw)
}

var generatedAt = regexp.MustCompile(`"generated_at":"[^"]*"`)

// normalize blanks the ids a caller chose, and the clock, so an answer
// about an unreachable id compares with the same answer about a missing
// one.
func normalize(s string, ids ...string) string {
	for _, id := range ids {
		if id != "" {
			s = strings.ReplaceAll(s, id, "<id>")
		}
	}
	return generatedAt.ReplaceAllString(s, `"generated_at":"<t>"`)
}

// assertLikeMissing is the existence contract: what a caller is told about
// an id they cannot reach is exactly what they are told about an id that
// never existed.
func assertLikeMissing(t *testing.T, what string, foreign, missing outcome, foreignID, missingID string) {
	t.Helper()
	f := foreign
	f.Message, f.Result = normalize(f.Message, foreignID, missingID), normalize(f.Result, foreignID, missingID)
	m := missing
	m.Message, m.Result = normalize(m.Message, foreignID, missingID), normalize(m.Result, foreignID, missingID)
	if f != m {
		t.Errorf("%s: unreachable %s answers %+v, a missing id answers %+v", what, foreignID, foreign, missing)
	}
}

// missingID is an id of the same shape as real that no row has.
func missingID(real string) string {
	kind, _, _ := strings.Cut(real, "-")
	return kind + "-0000000000"
}

// observe opens a read-only attach and returns its ack, refused or not.
func observe(t *testing.T, client *ssh.Client, runID string) protocol.AttachResponse {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("observer session: %v", err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.RequestPty("xterm-256color", 30, 120, ssh.TerminalModes{}); err != nil {
		t.Fatalf("observer pty-req: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RequestSubsystem(protocol.SubsystemAttach); err != nil {
		t.Fatalf("observer subsystem: %v", err)
	}
	header, err := json.Marshal(protocol.AttachRequest{
		RunID: runID, ReadOnly: true, Cols: 120, Rows: 30,
		ControlSessionID: "access-observer-" + runID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(append(header, '\n')); err != nil {
		t.Fatalf("write observer request: %v", err)
	}
	line, err := protocol.ReadLine(bufio.NewReader(stdout))
	if err != nil {
		t.Fatalf("read observer ack: %v", err)
	}
	var ack protocol.AttachResponse
	if err := json.Unmarshal(line, &ack); err != nil {
		t.Fatalf("decode observer ack %q: %v", line, err)
	}
	return ack
}

// eventStream is one aether-events subscription with its events pumped
// into a buffer.
type eventStream struct {
	ack protocol.SubscribeResponse

	mu   sync.Mutex
	seen []protocol.Event
}

func subscribe(t *testing.T, client *ssh.Client, req protocol.SubscribeRequest) *eventStream {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("events session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RequestSubsystem(protocol.SubsystemEvents); err != nil {
		t.Fatalf("aether-events subsystem: %v", err)
	}
	header, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(append(header, '\n')); err != nil {
		t.Fatalf("write subscribe request: %v", err)
	}
	r := bufio.NewReader(stdout)
	line, err := protocol.ReadLine(r)
	if err != nil {
		t.Fatalf("read subscribe ack: %v", err)
	}
	s := &eventStream{}
	if err := json.Unmarshal(line, &s.ack); err != nil {
		t.Fatalf("decode subscribe ack %q: %v", line, err)
	}
	if s.ack.OK {
		go func() {
			for {
				line, err := protocol.ReadLine(r)
				if err != nil {
					return
				}
				var e protocol.Event
				if json.Unmarshal(line, &e) == nil {
					s.mu.Lock()
					s.seen = append(s.seen, e)
					s.mu.Unlock()
				}
			}
		}()
	}
	return s
}

func (s *eventStream) events() []protocol.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.Event(nil), s.seen...)
}

func (s *eventStream) ackOutcome() outcome {
	if s.ack.OK {
		return outcome{Result: "ok"}
	}
	return outcome{Code: s.ack.Code, Message: s.ack.Error}
}

// waitMarkers waits until the stream carried an event with every marker,
// or the deadline passes, and reports the ones that never came.
func (s *eventStream) waitMarkers(deadline time.Time, markers ...string) []string {
	for {
		var missing []string
		for _, m := range markers {
			found := false
			for _, e := range s.events() {
				if bytes.Contains(e.Payload, []byte(m)) {
					found = true
					break
				}
			}
			if !found {
				missing = append(missing, m)
			}
		}
		if len(missing) == 0 || time.Now().After(deadline) {
			return missing
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// gitAs runs git with member's key and returns its exit status and
// stderr instead of failing.
func gitAs(m *wsaMember, dir string, args ...string) (int, string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+m.keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o LogLevel=ERROR",
		"GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return exit.ExitCode(), stderr.String()
	case err != nil:
		return -1, err.Error()
	}
	return 0, stderr.String()
}

// coordCall is one call on a run's coordination socket, paced like every
// other socket caller in the suite, as an outcome.
func coordCall(ctx context.Context, socket, method string, params any) outcome {
	var raw json.RawMessage
	return outcomeOf(pacedCall(ctx, socket, method, params, &raw), raw)
}
