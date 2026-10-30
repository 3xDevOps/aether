package acphost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acpkeeper"
)

// agentInput ends the connection on a failed write: the agent takes no more
// input, and its output may stay open long after. It also reports when a
// turn's session/prompt is on the wire.
type agentInput struct {
	io.WriteCloser
	output *io.PipeWriter
	broken atomic.Bool

	mu     sync.Mutex
	unsent chan struct{}
	// promptID is the JSON-RPC id of the session/prompt awaiting its answer.
	promptID string
	// promptTag and steerTag name what the next session/prompt and the next
	// steer deliver, and promptTurn is the turn that prompt starts, for a
	// writer that records them.
	promptTag, steerTag string
	promptTurn          int64
}

// keeperWriter is a keeper attachment: it records what a request delivers
// and acks the agent's output once it is handled.
type keeperWriter interface {
	WriteTagged(p []byte, tag string, turn int64) (int, error)
	Handled(seq uint64)
}

type rpcHead struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
}

func (in *agentInput) Write(p []byte) (int, error) {
	var m rpcHead
	method := ""
	for _, name := range []string{acp.AgentMethodSessionPrompt, acpkeeper.AdoptMethod, methodSteering} {
		if bytes.Contains(p, []byte(`"`+name+`"`)) && json.Unmarshal(p, &m) == nil && m.Method == name {
			method = name
			break
		}
	}
	prompt := method == acp.AgentMethodSessionPrompt || method == acpkeeper.AdoptMethod
	tag, turn := "", int64(0)
	in.mu.Lock()
	switch {
	case prompt:
		// Before the write: the agent can answer before Write returns.
		in.promptID = string(m.ID)
		tag, in.promptTag = in.promptTag, ""
		turn, in.promptTurn = in.promptTurn, 0
	case method == methodSteering:
		tag, in.steerTag = in.steerTag, ""
	}
	in.mu.Unlock()
	var n int
	var err error
	if kept, ok := in.WriteCloser.(keeperWriter); ok && (tag != "" || turn != 0) {
		n, err = kept.WriteTagged(p, tag, turn)
	} else {
		n, err = in.WriteCloser.Write(p)
	}
	if err != nil {
		in.broken.Store(true)
		_ = in.output.Close()
	}
	if prompt {
		in.mu.Lock()
		if in.unsent != nil {
			close(in.unsent)
			in.unsent = nil
		}
		in.mu.Unlock()
	}
	return n, err
}

// answersPrompt reports whether line, one line of the agent's output, is its
// answer to the latest session/prompt.
func (in *agentInput) answersPrompt(line []byte) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	var m rpcHead
	if in.promptID == "" || !bytes.Contains(line, []byte(`"id"`)) || json.Unmarshal(line, &m) != nil ||
		m.Method != "" || string(m.ID) != in.promptID {
		return false
	}
	in.promptID = ""
	return true
}

// sendingPrompt holds a cancel back until the next session/prompt, which
// delivers what tag names and starts turn, is written, or until the returned
// release is called.
func (in *agentInput) sendingPrompt(tag string, turn int64) func() {
	unsent := make(chan struct{})
	in.mu.Lock()
	in.unsent = unsent
	in.promptTag, in.promptTurn = tag, turn
	in.mu.Unlock()
	return func() {
		in.mu.Lock()
		defer in.mu.Unlock()
		if in.unsent == unsent {
			close(unsent)
			in.unsent = nil
		}
	}
}

func (in *agentInput) promptUnsent() <-chan struct{} {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.unsent
}

// gatedReader holds back reads until open is closed: the SDK's receive loop
// starts inside NewConnection and reads the logger SetLogger writes.
type gatedReader struct {
	open <-chan struct{}
	r    io.Reader
}

func (g gatedReader) Read(p []byte) (int, error) {
	<-g.open
	return g.r.Read(p)
}

// relay copies the agent's output to the SDK, with answeredMarker behind the
// answer to each session/prompt.
func (c *Conn) relay(dst *io.PipeWriter, src io.Reader) {
	br := bufio.NewReaderSize(src, 64<<10)
	lineStart := true
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			ended := chunk[len(chunk)-1] == '\n'
			// Matched before the SDK sees the answer, which can start the
			// next prompt.
			answered := ended && lineStart && c.w.answersPrompt(chunk)
			if answered {
				c.answers.Add(1)
			}
			_, werr := dst.Write(chunk)
			if werr == nil && answered {
				_, werr = dst.Write(answeredLine)
			}
			if werr != nil {
				_ = dst.CloseWithError(werr)
				return
			}
			lineStart = ended
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			_ = dst.Close()
			return
		case err != nil:
			_ = dst.CloseWithError(err)
			return
		}
	}
}
