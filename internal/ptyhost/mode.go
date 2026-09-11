package ptyhost

import (
	"slices"
	"strconv"
)

// A client that attaches mid-run rebuilds its screen from the replay ring,
// which is a byte tail: whatever the agent set up before those bytes is
// gone. Terminal modes are set once at startup and never repeated, so the
// tail almost never carries them, and a dashboard terminal resets itself
// before every replay - so without this the reattached terminal disagrees
// with the PTY about how it behaves.
//
// Bracketed paste is the mode that shows: with DECSET 2004 lost, a client
// sends a pasted block as bare carriage returns, and an agent that reads
// them as Enter submits the first line and treats the rest as more
// answers. The others are tracked because the same reasoning applies -
// a client that thinks the cursor is hidden, that autowrap is off, or
// that mouse reporting is on is wrong in a way only the agent can fix,
// and it cannot, because it has no idea anyone reattached.
//
// The alternate screen buffer is deliberately absent. Replaying DECSET
// 1049 is not a flag but a buffer switch that saves the cursor and clears
// what it switches to, so asserting it around a tail that was captured
// inside it would change what the tail draws. An agent on the alternate
// screen repaints on the resize nudge instead.
//
// Only the modes below are tracked. An unrecognised private mode passes
// through to the client untouched, as every byte does; it is simply not
// remembered for the next attach.
var trackedModes = map[int]bool{
	2004: true, // bracketed paste
	25:   true, // cursor visibility (DECTCEM)
	7:    true, // autowrap (DECAWM)
	1000: true, // mouse: button events
	1002: true, // mouse: button events with drag
	1003: true, // mouse: any-motion tracking
	1006: true, // mouse: SGR extended coordinates
	1016: true, // mouse: SGR pixel coordinates
}

// modeDefaults are the states a terminal powers up in, which is also what
// a client that just reset itself holds. A mode sitting at its default
// needs no preamble: saying so would cost bytes and change nothing. The
// two that start on are autowrap and the cursor; everything else tracked
// here starts off.
var modeDefaults = map[int]bool{7: true, 25: true}

type modeScanState uint8

const (
	modeNormal modeScanState = iota
	modeEsc
	modeCSI
	modeParams
	modeString
	modeStringEsc
)

// maxModeParams bounds the parameter bytes carried between chunks. A
// private mode sequence is a handful of digits and separators; anything
// longer is not one, so the scan gives up on it rather than growing.
const maxModeParams = 64

// modeScanner follows DECSET and DECRST in the PTY's output and keeps the
// latest state of the modes a reattaching client has to be told about. Its
// state survives chunks because PTY reads can split an escape, exactly as
// titleScanner's does.
type modeScanner struct {
	state  modeScanState
	params []byte
	// set holds a tracked mode only once it has been seen, so a mode the
	// agent never touched is never asserted on its behalf.
	set map[int]bool
}

func (s *modeScanner) scan(p []byte) {
	for _, b := range p {
		s.byte(b)
	}
}

func (s *modeScanner) byte(b byte) {
	switch s.state {
	case modeNormal:
		if b == esc {
			s.state = modeEsc
		}
	case modeEsc:
		switch b {
		case '[':
			s.state = modeCSI
		case ']', 'P', 'X', '^', '_':
			// OSC, DCS, SOS, PM, APC. Everything up to the string
			// terminator is payload - a window title or a device reply -
			// which a terminal never executes, so neither may this.
			s.state = modeString
		case esc:
			s.state = modeEsc
		default:
			s.state = modeNormal
		}
	case modeCSI:
		// Only the private form, CSI ? ... h/l, sets these modes.
		if b == '?' {
			s.state = modeParams
			s.params = s.params[:0]
			return
		}
		if b == esc {
			s.state = modeEsc
			return
		}
		s.state = modeNormal
	case modeParams:
		switch {
		case b == 'h' || b == 'l':
			s.apply(b == 'h')
			s.state = modeNormal
		case (b >= '0' && b <= '9') || b == ';':
			if len(s.params) >= maxModeParams {
				s.state = modeNormal
				s.params = s.params[:0]
				return
			}
			s.params = append(s.params, b)
		case b == esc:
			s.state = modeEsc
			s.params = s.params[:0]
		default:
			// Any other final byte is a different private sequence.
			s.state = modeNormal
			s.params = s.params[:0]
		}
	case modeString:
		switch b {
		case bel:
			s.state = modeNormal
		case esc:
			s.state = modeStringEsc
		}
	case modeStringEsc:
		switch b {
		case '\\', bel:
			// ST, or the BEL an OSC may end with instead.
			s.state = modeNormal
		case esc:
			s.state = modeStringEsc
		default:
			s.state = modeString
		}
	}
}

// apply records every tracked mode in the parameter list. One sequence can
// carry several, as `CSI ? 1002 ; 1006 h` does.
func (s *modeScanner) apply(on bool) {
	for _, field := range splitParams(s.params) {
		mode, err := strconv.Atoi(field)
		if err != nil || !trackedModes[mode] {
			continue
		}
		if s.set == nil {
			s.set = make(map[int]bool, len(trackedModes))
		}
		s.set[mode] = on
	}
	s.params = s.params[:0]
}

func splitParams(p []byte) []string {
	if len(p) == 0 {
		return nil
	}
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == ';' {
			if i > start {
				out = append(out, string(p[start:i]))
			}
			start = i + 1
		}
	}
	return out
}

// preamble renders the tracked modes that differ from a fresh terminal's
// defaults as the DECSET and DECRST sequences that restore them. It is
// written ahead of the replay so the client ends up in the state the agent
// put the PTY in, whether or not the bytes that did so are still in the
// ring. Modes are emitted in ascending order so the bytes are stable and
// testable.
func (s *modeScanner) preamble() []byte {
	if len(s.set) == 0 {
		return nil
	}
	var on, off []int
	for mode, state := range s.set {
		if state == modeDefaults[mode] {
			continue
		}
		if state {
			on = append(on, mode)
		} else {
			off = append(off, mode)
		}
	}
	return append(modeSequence(on, 'h'), modeSequence(off, 'l')...)
}

func modeSequence(modes []int, final byte) []byte {
	if len(modes) == 0 {
		return nil
	}
	slices.Sort(modes)
	out := []byte{esc, '[', '?'}
	for i, mode := range modes {
		if i > 0 {
			out = append(out, ';')
		}
		out = strconv.AppendInt(out, int64(mode), 10)
	}
	return append(out, final)
}
