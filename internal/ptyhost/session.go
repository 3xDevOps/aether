package ptyhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// maxClientBuffer bounds the per-client pending output; a client that falls
// this far behind is force-detached so it can never block the agent.
const maxClientBuffer = 4 << 20

// resizeTimeout bounds each att.Resize call: geometry reconciliation runs
// off the session lock and a hung runtime must never wedge it forever.
const resizeTimeout = 5 * time.Second

// echoWindow bounds how long the terminal's echo of written input is still
// expected back. The line discipline echoes as it accepts the bytes, so
// anything later is the agent's own output; without the bound a terminal
// with echo off - every full-screen agent - would leave the expectation
// standing against whatever the agent printed next.
const echoWindow = time.Second

// paintQuiet is how long agent output stays liveness-silent after the
// session pokes the PTY with a redraw nudge. A TUI answers the nudge with
// a full repaint; those bytes prove nothing about the agent's progress,
// so they must not clear a stall the way a real answer would. Tests
// shorten it to keep assertions off the real window.
var paintQuiet = 3 * time.Second

// maxPendingEcho caps the echo the session tracks at once. An expectation
// past it is dropped whole: the bytes then count as the agent's, which only
// costs a stall one more threshold, where an unbounded queue would grow for
// as long as anyone kept steering.
const maxPendingEcho = 8 << 10

var errSlowClient = errors.New("ptyhost: client too slow, detached")

type pendingOutputEvent struct {
	end    int
	raw    bool
	marker string
}

// session is one persistent PTY session: the adopted attachment, its pump,
// the replay ring, the transcript, and the attached clients.
type session struct {
	run SessionKey
	att runtime.Attachment
	tr  *castWriter

	stdinMu sync.Mutex
	stdin   io.WriteCloser

	mu               sync.Mutex
	clients          map[*client]struct{}
	ring             *ring
	cols             uint // desired effective geometry
	rows             uint
	acceptedCols     uint // last runtime geometry accepted
	acceptedRows     uint
	lastResizeCursor uint64
	geoGen           uint64  // bumped whenever the PTY must be (re)sized
	geoApplied       uint64  // last geoGen an applier has picked up
	geoTold          [2]uint // last size the clients were told about
	ended            bool
	stopped          bool
	lastOut          time.Time
	paintQuietUntil  time.Time
	done             chan struct{}
	title            titleScanner
	modes            modeScanner
	screen           *terminalScreen
	pendingScreen    []byte
	pendingEvents    []pendingOutputEvent
	finalSnapshot    ScreenSnapshot
	onTitle          func(string)

	// pendingEcho is the echo the terminal still owes for input the server
	// wrote to the agent - an injected line, or a member's keystrokes. The
	// line discipline echoes them back through the PTY even when the agent
	// never reads it, so those bytes are not evidence the agent is alive;
	// see expectEcho and consumeEcho. echoDeadline expires the expectation.
	pendingEcho  []byte
	echoDeadline time.Time

	// resizeActive keeps new geometry-aware clients from joining between
	// the resize boundary and its resolution. resizeDone is closed only
	// after every existing client's boundary has been resolved.
	resizeActive bool
	resizeDone   chan struct{}

	// resizeMu serializes att.Resize applications. A boundary is queued for
	// geometry-aware clients before each application, so output delivery can
	// continue without taking this lock while the runtime is in flight.
	resizeMu sync.Mutex
}

func (s *session) pump() {
	s.mu.Lock()
	att := s.att
	stopped := s.stopped
	s.mu.Unlock()
	if stopped || att == nil {
		return
	}
	out := att.Stdout()
	buf := make([]byte, 32*1024)
	for {
		n, err := out.Read(buf)
		if n > 0 {
			s.deliver(buf[:n])
		}
		if err != nil {
			s.end()
			return
		}
	}
}

func (s *session) deliver(p []byte) {
	if len(p) == 0 {
		return
	}
	now := time.Now()
	s.mu.Lock()
	if s.stopped || s.ended {
		s.mu.Unlock()
		return
	}
	// These scanners and clocks describe when bytes arrived from the PTY,
	// rather than when a slow geometry RPC eventually lets the emulator
	// consume them.
	if s.consumeEcho(p, now) && now.After(s.paintQuietUntil) {
		s.lastOut = now
	}
	s.title.scan(p, s.onTitle)
	for {
		if s.resizeActive {
			// Keep the one deferred buffer bounded. In particular, do not
			// publish p to the ring or clients before it fits: an attach
			// acknowledged at that cursor must never miss bytes that the
			// live screen has not consumed.
			if len(s.pendingScreen)+len(p) > maxClientBuffer {
				done := s.resizeDone
				s.mu.Unlock()
				if done == nil {
					// resizeActive and resizeDone are kept in lockstep. Be
					// defensive if a hand-built test session violates it.
					s.mu.Lock()
					if s.stopped || s.ended {
						s.mu.Unlock()
						return
					}
					s.resizeActive = false
					continue
				}
				<-done
				s.mu.Lock()
				if s.stopped || s.ended {
					s.mu.Unlock()
					return
				}
				continue
			}
			s.queuePendingOutputLocked(p, true, "")
			// Raw consumers (notably the adapter tap) must remain live while
			// the runtime RPC is in flight. Geometry-aware clients already
			// have an ordered boundary in front of this output, so enqueueing
			// it now preserves the same order for them too.
			for c := range s.clients {
				c.enqueue(p)
			}
			s.mu.Unlock()
			return
		}
		s.commitOutputLocked(p)
		s.mu.Unlock()
		return
	}
}

// queuePendingOutputLocked appends one ordered output event to the single
// bounded byte buffer held across a resize. raw controls ring publication;
// markers belong only to the transcript.
func (s *session) queuePendingOutputLocked(p []byte, raw bool, marker string) {
	s.pendingScreen = append(s.pendingScreen, p...)
	s.pendingEvents = append(s.pendingEvents, pendingOutputEvent{
		end:    len(s.pendingScreen),
		raw:    raw,
		marker: marker,
	})
}

// commitOutputLocked applies raw PTY output to every durable/live
// representation. Callers hold s.mu.
func (s *session) commitOutputLocked(p []byte) {
	if len(p) == 0 {
		return
	}
	if s.screen != nil {
		s.screen.write(p)
	}
	s.modes.scan(p)
	if s.ring != nil {
		s.ring.write(p)
	}
	if s.tr != nil {
		s.tr.output(p)
	}
	for c := range s.clients {
		c.enqueue(p)
	}
}

// commitPendingOutputLocked applies output withheld while a resize RPC was in
// flight. The caller has already sent this data to live clients. Callers hold
// s.mu.
func (s *session) commitPendingOutputLocked(p []byte) {
	if len(p) == 0 {
		return
	}
	if s.screen != nil {
		s.screen.write(p)
	}
	s.modes.scan(p)
	if len(s.pendingEvents) == 0 {
		if s.ring != nil {
			s.ring.write(p)
		}
		if s.tr != nil {
			s.tr.output(p)
		}
		return
	}
	offset := 0
	for _, event := range s.pendingEvents {
		end := event.end
		if end < offset {
			continue
		}
		if end > len(p) {
			end = len(p)
		}
		chunk := p[offset:end]
		if event.raw && s.ring != nil {
			s.ring.write(chunk)
		}
		if s.tr != nil {
			s.tr.output(chunk)
			if event.marker != "" {
				s.tr.marker(event.marker)
			}
		}
		offset = end
		if offset == len(p) {
			break
		}
	}
	if offset < len(p) {
		if s.ring != nil {
			s.ring.write(p[offset:])
		}
		if s.tr != nil {
			s.tr.output(p[offset:])
		}
	}
}

// end marks the session ended after the agent exited: the transcript closes
// and every attachment drains to EOF, but the session stays queryable until
// StopSession.
func (s *session) end() {
	// Finalization must not race an in-flight Resize: otherwise the resize
	// applier could write to a nil ring or the final snapshot could capture
	// the old grid while deferred output is still outstanding.
	s.resizeMu.Lock()
	s.mu.Lock()
	if s.ended || s.stopped {
		s.mu.Unlock()
		s.resizeMu.Unlock()
		return
	}
	s.ended = true
	s.finishLocked()
	s.ring = nil // no further attaches: release the replay buffer
	if s.done != nil {
		close(s.done)
	}
	s.mu.Unlock()
	s.resizeMu.Unlock()
}

func (s *session) finishLocked() {
	if len(s.pendingScreen) > 0 {
		s.commitPendingOutputLocked(s.pendingScreen)
		s.pendingEvents = nil
		s.pendingScreen = nil
	}
	if s.screen != nil {
		s.finalSnapshot = makeScreenSnapshot(s.screen, s.modes)
		s.screen.dispose()
		s.screen = nil
	}
	tr := s.tr
	s.tr = nil
	if tr != nil {
		_ = tr.close()
	}
	for c := range s.clients {
		c.close(nil)
	}
	if done := s.resizeDone; done != nil {
		s.resizeDone = nil
		s.resizeActive = false
		close(done)
	} else {
		s.resizeActive = false
	}
}

func (s *session) stop() {
	s.resizeMu.Lock()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		s.resizeMu.Unlock()
		return
	}
	s.stopped = true
	s.finishLocked()
	// The sessions map keeps a lightweight stopped entry so StopSession
	// remains idempotent. Release all attachment and transcript state that
	// would otherwise retain runtime stream buffers for the life of the host.
	att := s.att
	s.ring = nil
	s.clients = nil
	s.att = nil
	s.stdin = nil
	s.title = titleScanner{}
	s.modes = modeScanner{}
	s.pendingEcho = nil
	s.onTitle = nil
	if !s.ended && s.done != nil {
		close(s.done)
	}
	s.mu.Unlock()
	s.resizeMu.Unlock()
	if att != nil {
		_ = att.Close()
	}

}

func (s *session) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped && !s.ended
}

func (s *session) lastOutput() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return time.Time{}, false
	}
	return s.lastOut, true
}
func (s *session) purgeSnapshot() {
	s.mu.Lock()
	s.finalSnapshot = ScreenSnapshot{}
	s.mu.Unlock()
}
func (s *session) snapshot() (ScreenSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.finalSnapshot.Data) > 0 {
		return cloneScreenSnapshot(s.finalSnapshot), nil
	}
	if s.screen == nil {
		return ScreenSnapshot{}, errors.New("ptyhost: terminal snapshot unavailable")
	}
	return makeScreenSnapshot(s.screen, s.modes), nil
}

func (s *session) addClient(c *client) error {
	for {
		s.mu.Lock()
		if s.resizeActive || s.resizeDone != nil {
			done := s.resizeDone
			s.mu.Unlock()
			// Do not let a fresh attach's replay bypass an active
			// geometry boundary. Waiting is outside mu so output,
			// resize completion, and existing clients continue.
			if done != nil {
				<-done
			}
			continue
		}
		if s.stopped {
			s.mu.Unlock()
			return ErrNoSession
		}
		if s.ended {
			s.mu.Unlock()
			return ErrSessionEnded
		}
		c.replayCols, c.replayRows = s.acceptedCols, s.acceptedRows
		if c.replayCols == 0 || c.replayRows == 0 {
			c.replayCols, c.replayRows = s.cols, s.rows
		}
		// A resumed terminal can consume a raw gap only when no accepted
		// resize occurred before that gap. Resize events are not in the raw
		// ring, so a non-empty gap crossing one must rebuild the screen.
		if c.resume {
			if missed, ok := s.ring.since(c.cursor); ok &&
				(!c.snapshot || len(missed) == 0 || c.cursor >= s.lastResizeCursor) {
				c.replay = missed
				c.resumed = true
			}
		}
		if !c.resumed && c.snapshot {
			c.replay = makeScreenSnapshot(s.screen, s.modes).Data
		} else if !c.resumed {
			// Everyone else rebuilds from the ring, which is a byte tail:
			// modes the agent set at startup are long gone from it, and the
			// preamble puts them back ahead of the replay.
			c.replay = append(s.modes.preamble(), s.ring.bytes()...)
		}
		// Where the replay leaves this client, so it can say where it got
		// to if it comes back.
		c.cursor = s.ring.written
		s.clients[c] = struct{}{}
		// Any join can change who imposes, not just this client: the mirror
		// that was alone here a moment ago no longer is. Raw replay needs
		// a redraw; snapshots and successful resumes already hold the screen.
		s.reconcileLocked(!c.snapshot && !c.resumed && c.cols != 0 && c.rows != 0)
		s.mu.Unlock()
		return nil
	}
}

func (s *session) removeClient(c *client) {
	s.mu.Lock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		// Leaving can promote the client left behind, so the size is
		// recomputed whoever it was that went.
		if !s.ended && !s.stopped {
			s.reconcileLocked(false)
		}
	}
	s.mu.Unlock()
	c.close(nil)
}

func (s *session) resizeClient(c *client, cols, rows uint) {
	if validateScreenDimensions(cols, rows) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[c]; !ok {
		return
	}
	c.cols, c.rows = cols, rows
	if s.imposesNow(c) && !s.ended && !s.stopped {
		s.reconcileLocked(s.geoApplied != s.geoGen)
	}
}

// reconcileLocked recomputes the effective PTY size as the per-dimension
// minimum over the clients that impose one (see imposesNow), records it, and
// schedules the att.Resize application off the lock (a slow runtime resize
// must never stall output delivery). With no such client the size stays
// unchanged; a fresh screen-bearing client can still force a redraw nudge at
// that current size.
func (s *session) reconcileLocked(force bool) {
	var cols, rows uint
	found := false
	for c := range s.clients {
		if !s.imposesNow(c) {
			continue
		}
		if !found {
			cols, rows = c.cols, c.rows
			found = true
			continue
		}
		cols = min(cols, c.cols)
		rows = min(rows, c.rows)
	}
	if !found {
		if !force {
			return
		}
		cols, rows = s.cols, s.rows
	}
	changed := cols != s.cols || rows != s.rows
	if !changed && !force {
		return
	}
	s.cols, s.rows = cols, rows
	s.geoGen++
	// One applier owns each scheduled boundary. A resize update that arrives
	// while its RPC is in flight only advances geoGen; the applier schedules
	// the next generation after it commits this one.
	if s.resizeDone == nil {
		s.resizeDone = make(chan struct{})
		go s.applyResize()
	}
}

// applyResize applies one recorded geometry to the attachment with a redraw
// nudge (rows-1 then rows) so TUIs repaint. Before entering the runtime call
// it inserts an unresolved boundary in every existing GeometryWriter queue.
// Output delivery remains live for raw clients while the runtime is in
// flight, but screen, ring, and cast publication wait for the accepted
// geometry. Each RPC is bounded by resizeTimeout.
func (s *session) applyResize() {
	s.resizeMu.Lock()
	s.mu.Lock()
	if s.geoApplied == s.geoGen || s.ended || s.stopped || s.att == nil {
		done := s.resizeDone
		s.resizeActive = false
		s.resizeDone = nil
		if done != nil {
			close(done)
		}
		s.mu.Unlock()
		s.resizeMu.Unlock()
		return
	}
	gen := s.geoGen
	s.geoApplied = gen
	cols, rows := s.cols, s.rows
	att := s.att
	done := s.resizeDone
	if done == nil {
		done = make(chan struct{})
		s.resizeDone = done
	}
	s.resizeActive = true
	var targets []resizeTarget
	for c := range s.clients {
		boundary := newResizeBoundary()
		if c.enqueueResizeBoundary(boundary) {
			targets = append(targets, resizeTarget{client: c, boundary: boundary})
		}
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), resizeTimeout)
	if rows > 1 {
		_ = att.Resize(ctx, cols, rows-1)
	}
	rpcOK := att.Resize(ctx, cols, rows) == nil
	cancel()

	s.mu.Lock()
	applied := rpcOK && !s.ended && !s.stopped
	s.paintQuietUntil = time.Now().Add(paintQuiet)
	changed := cols != s.acceptedCols || rows != s.acceptedRows
	if applied {
		s.acceptedCols, s.acceptedRows = cols, rows
		if s.screen != nil {
			_ = s.screen.resize(cols, rows)
		}
		// Keep the cast's resize event before every output emitted while the
		// RPC was pending. This is the same order used by the emulator.
		if changed && s.tr != nil {
			s.tr.resize(cols, rows)
		}
	} else if s.geoGen == gen && gen > 0 {
		// Keep this generation eligible for a later retry without spinning a
		// retry loop here. A subsequent client resize or attach calls force.
		s.geoApplied = gen - 1
	}
	if len(s.pendingScreen) > 0 {
		s.commitPendingOutputLocked(s.pendingScreen)
		s.pendingEvents = nil
		s.pendingScreen = nil
	}
	if applied && changed && s.ring != nil {
		// A resume cursor below this point crosses the geometry event and
		// must rebuild from the now-committed screen rather than consume a
		// raw gap against the old grid.
		s.lastResizeCursor = s.ring.written
	}
	tell := applied && s.geoTold != [2]uint{cols, rows}
	if tell {
		s.geoTold = [2]uint{cols, rows}
	}
	signals := make([]*resizeBoundary, 0, len(targets))
	for _, target := range targets {
		signals = append(signals, target.client.resolveResizeBoundary(target.boundary, cols, rows, tell)...)
	}
	// Every completed RPC releases its boundary and lets output between this
	// operation and a newer one use the geometry the runtime just accepted.
	s.resizeActive = false
	s.resizeDone = nil
	if done != nil {
		close(done)
	}
	next := s.geoGen != gen && !s.ended && !s.stopped && s.att != nil
	if next {
		// Reserve the next applier before dropping s.mu; otherwise a resize
		// update in this small handoff window could launch a duplicate
		// applier that closes the next generation's boundary.
		s.resizeDone = make(chan struct{})
	}
	s.mu.Unlock()

	// Signal only after results are installed, but before releasing resizeMu:
	// end/stop can then safely finalize immediately after this operation.
	for _, boundary := range signals {
		boundary.signal()
	}
	s.resizeMu.Unlock()
	if next {
		go s.applyResize()
	}
}

// writeStdin forwards keystrokes to the agent; false once the session is
// over. The terminal echoes them back exactly as it echoes an injected
// line, so they register the same expectation: a member typing at a hung
// agent is not the agent talking. stdinMu is held across the registration
// so the queued echoes stay in the order the writes reach the PTY.
func (s *session) writeStdin(p []byte) bool {
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	s.mu.Lock()
	if s.ended || s.stopped || s.stdin == nil {
		s.mu.Unlock()
		return false
	}
	stdin := s.stdin
	s.expectEcho(p, time.Now())
	s.mu.Unlock()

	_, err := stdin.Write(p)
	if err != nil {
		s.dropEcho()
	}
	return err == nil
}

func (s *session) annotateInjection(actorName, actorColor, message string) error {
	safeActor, safeMessage := bannerText(actorName), bannerText(message)
	banner := renderBanner(safeActor, actorColor, safeMessage)
	marker := "inject by " + safeActor + ": " + safeMessage
	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return ErrNoSession
		}
		if s.ended {
			s.mu.Unlock()
			return ErrSessionEnded
		}
		if s.resizeActive && len(s.pendingScreen)+len(banner) > maxClientBuffer {
			done := s.resizeDone
			if done == nil {
				s.resizeActive = false
				s.mu.Unlock()
				continue
			}
			s.mu.Unlock()
			<-done
			continue
		}
		if s.resizeActive {
			s.queuePendingOutputLocked(banner, false, marker)
		} else {
			if s.screen != nil {
				s.screen.write(banner)
			}
			if s.tr != nil {
				s.tr.output(banner)
				s.tr.marker(marker)
			}
		}
		for c := range s.clients {
			c.enqueue(banner)
		}
		s.mu.Unlock()
		return nil
	}
}

func (s *session) inject(actorName, actorColor, message, submit string) error {
	line := []byte(message + submit)
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	s.mu.Lock()
	if s.stopped || s.stdin == nil {
		s.mu.Unlock()
		return ErrNoSession
	}
	if s.ended {
		s.mu.Unlock()
		return ErrSessionEnded
	}
	stdin := s.stdin
	tr := s.tr
	// Neither the banner nor the echo the terminal owes us may touch
	// lastOut: stall detection reads that clock, and counting the server's
	// own bytes would clear a stall for an agent that never answered.
	s.expectEcho(line, time.Now())
	s.mu.Unlock()
	n, err := stdin.Write(line)
	if err != nil {
		s.dropEcho()
		return fmt.Errorf("ptyhost: inject stdin write: %w", err)
	}
	if n != len(line) {
		s.dropEcho()
		return fmt.Errorf("ptyhost: inject stdin write: %w", io.ErrShortWrite)
	}
	if err := s.annotateInjection(actorName, actorColor, message); err != nil {
		// The write above already accepted the full line: a session that
		// ended in this window must not turn delivered input into a
		// reported failure, which would invite a double-submitting retry.
		// The banner has no viewers on a wound-down session, but the
		// transcript still takes the attribution marker.
		if errors.Is(err, ErrSessionEnded) || errors.Is(err, ErrNoSession) {
			if tr != nil {
				tr.lateMarker("inject by " + bannerText(actorName) + ": " + bannerText(message))
			}
			return nil
		}
		return err
	}
	return nil
}

// expectEcho queues the bytes the terminal will echo back for input the
// server just wrote to the agent's stdin. The line discipline rewrites
// every lone CR or LF as CRLF (ICRNL and ONLCR) and renders any other
// control byte in hat notation (ECHOCTL), TAB excepted, so a multi-line or
// control-bearing steer echoes as something quite unlike what was written.
// The editing characters are left out: DEL and the other VERASE/VKILL
// bindings are consumed by the line editor, which repaints rather than
// echoes them, so they take the divergence path and cost a stall one more
// threshold instead of being modelled wrongly here. An expectation too
// large to hold is dropped whole rather than half-matched. Callers hold
// mu, and stdinMu so the queue keeps the order the writes reach the PTY.
func (s *session) expectEcho(in []byte, now time.Time) {
	echo := make([]byte, 0, len(in)+8)
	for _, b := range in {
		switch {
		case b == '\r' || b == '\n':
			echo = append(echo, '\r', '\n')
		case b == '\t':
			echo = append(echo, b)
		case b < 0x20:
			echo = append(echo, '^', b^0x40)
		default:
			echo = append(echo, b)
		}
	}
	if len(s.pendingEcho)+len(echo) > maxPendingEcho {
		s.pendingEcho = nil
		return
	}
	s.pendingEcho = append(s.pendingEcho, echo...)
	s.echoDeadline = now.Add(echoWindow)
}

// dropEcho forgets the queued echo after a failed stdin write: bytes that
// never reached the terminal are never echoed, and discounting them would
// eat the agent's own output instead.
func (s *session) dropEcho() {
	s.mu.Lock()
	s.pendingEcho = nil
	s.mu.Unlock()
}

// consumeEcho strips the echo the session is still owed from the head of p
// and reports whether anything is left over - that leftover is the agent
// talking. The terminal echoes written input back through the PTY even when
// the agent never reads it, so an echo alone must not refresh the liveness
// clock. Two things end the expectation: the first byte that diverges from
// it, and echoWindow passing, since a terminal that is going to echo does
// so as it accepts the bytes. Either way the bytes count as the agent's,
// which is the safe direction. Callers hold mu.
func (s *session) consumeEcho(p []byte, now time.Time) bool {
	if len(s.pendingEcho) > 0 && now.After(s.echoDeadline) {
		s.pendingEcho = nil
	}
	n := 0
	for n < len(p) && n < len(s.pendingEcho) && p[n] == s.pendingEcho[n] {
		n++
	}
	if n < len(p) && n < len(s.pendingEcho) {
		s.pendingEcho = nil
		return true
	}
	s.pendingEcho = s.pendingEcho[n:]
	return n < len(p)
}

// renderBanner renders an attributed injection banner shown to viewers and
// recorded in the transcript; it is never written to the agent's input.
func renderBanner(actorName, actorColor, message string) []byte {
	var b bytes.Buffer
	b.WriteString("\r\n")
	b.WriteString(attribution.ANSI(actorColor))
	fmt.Fprintf(&b, "\x1b[7m ▸ %s injects \x1b[0m %s\r\n", actorName, message)
	return b.Bytes()
}

// bannerText keeps user-controlled message bytes from becoming terminal
// control sequences in the transcript or another member's terminal.
func bannerText(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			fmt.Fprintf(&b, "\\x%02X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
