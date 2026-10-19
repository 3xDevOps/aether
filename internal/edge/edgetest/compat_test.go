package edgetest

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// rawAgent is a server agent written against the wire contract alone, as
// an older or newer build speaks it.
type rawAgent struct {
	t *testing.T
	c *websocket.Conn
}

// enrollRaw dials the control endpoint and answers the challenge with a
// hello naming version.
func (h *harness) enrollRaw(version int) (*rawAgent, error) {
	t := h.t
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := websocket.Dial(t.Context(), h.relayURL+edgeproto.PathServerControl, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	a := &rawAgent{t: t, c: c}
	ch, ok := a.read().(edgeproto.Challenge)
	if !ok {
		t.Fatal("no challenge")
	}
	sig, err := edgeproto.SignEnrollment(signer, h.relayURL, ch.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	a.send(edgeproto.Hello{Version: version, HostKey: signer.PublicKey().Marshal(), Signature: sig, AgentVersion: "v0.0.0-raw", Name: "raw"})
	_, data, err := c.Read(t.Context())
	if err != nil {
		return nil, err
	}
	if m, err := edgeproto.DecodeControl(data); err != nil || m.(edgeproto.Ready).State != edgeproto.StateUnclaimed {
		t.Fatalf("after hello: %+v %v", m, err)
	}
	return a, nil
}

func (a *rawAgent) read() edgeproto.Message {
	a.t.Helper()
	_, data, err := a.c.Read(a.t.Context())
	if err != nil {
		a.t.Fatal(err)
	}
	m, err := edgeproto.DecodeControl(data)
	if err != nil {
		a.t.Fatal(err)
	}
	return m
}

func (a *rawAgent) send(m edgeproto.Message) {
	a.t.Helper()
	data, err := edgeproto.EncodeControl(m)
	if err != nil {
		a.t.Fatal(err)
	}
	a.write(string(data))
}

func (a *rawAgent) write(data string) {
	a.t.Helper()
	if err := a.c.Write(a.t.Context(), websocket.MessageText, []byte(data)); err != nil {
		a.t.Fatal(err)
	}
}

// TestProtocolVersions enrolls agents speaking the oldest version the
// edge accepts, a version newer than the edge's and one below it.
func TestProtocolVersions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	old, err := h.enrollRaw(edgeproto.MinVersion)
	if err != nil {
		t.Fatalf("agent at MinVersion %d: %v", edgeproto.MinVersion, err)
	}
	// A message type from a later version is skipped, not fatal.
	old.write(`{"type":"server_metrics","cpu":0.5}`)
	old.send(edgeproto.Ping{})
	for {
		if _, ok := old.read().(edgeproto.Pong); ok {
			break
		}
	}

	if _, err = h.enrollRaw(edgeproto.Version + 1); err != nil {
		t.Fatalf("agent at version %d: %v", edgeproto.Version+1, err)
	}
	// The newer agent speaks the edge's version.
	if got := h.relay().Metrics().ServersByProtocol; got[edgeproto.MinVersion] != 1 || got[edgeproto.Version] != 1 || len(got) != 2 {
		t.Fatalf("servers by protocol = %v, want one at version %d and one at %d", got, edgeproto.MinVersion, edgeproto.Version)
	}

	_, err = h.enrollRaw(edgeproto.MinVersion - 1)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || !strings.Contains(ce.Reason, fmt.Sprintf("upgrade required: %d", edgeproto.MinVersion)) {
		t.Fatalf("agent below MinVersion: %v, want closed with upgrade required", err)
	}
}
