package ptyhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	xterm "github.com/gitpod-io/xterm-go"
)

// A snapshot deliberately keeps a small scrollback window. The viewport and
// this window are enough to reconnect the dashboard without replaying the
// redraw-heavy history that remains available through Replay.
const snapshotScrollback = 200

// These limits keep an untrusted PTY resize from turning a snapshot attach
// into an unbounded allocation. They are above ordinary terminal sizes while
// bounding both dimensions and the backing cell store (including scrollback).
const (
	maxScreenCols  = 4096
	maxScreenRows  = 4096
	maxScreenCells = 1 << 20
)

// Keep the suffix within xterm-go's own OSC/DCS handler payload limit. An
// overflow uses a synthetic string opener, so a fresh attach still consumes
// the remaining sequence rather than dropping bytes into visible output.
const maxSnapshotContinuation = xterm.ParserPayloadLimit
const maxCSIContinuation = 4 << 10

var errScreenDimensions = errors.New("ptyhost: terminal dimensions out of bounds")

// ScreenSnapshot is the compact terminal state used by a fresh dashboard
// attach and by a finished run. Data is a replayable VT stream, not a raw
// transcript tail.
type ScreenSnapshot struct {
	Cols uint
	Rows uint
	Data []byte
}

type terminalScreen struct {
	term  *xterm.Terminal
	addon *xterm.SerializeAddon
	cols  uint
	rows  uint

	// xterm-go deliberately keeps parser and UTF-8 decoder state private to
	// Terminal. This mirror tracks only the bytes that have not reached a
	// parser ground state, so a snapshot can hand them to a new Terminal.
	continuationParser   *xterm.EscapeSequenceParser
	continuation         []byte
	continuationOverflow bool
	continuationState    xterm.ParserState
	parserCodepoint      [1]uint32
	utf8Pending          [4]byte
	utf8PendingLen       int
	utf8PendingNeed      int
}

func validateScreenDimensions(cols, rows uint) error {
	if cols == 0 || rows == 0 || cols > maxScreenCols || rows > maxScreenRows {
		return errScreenDimensions
	}
	// xterm allocates rows plus scrollback lines, so bound that total rather
	// than only the visible viewport.
	if uint64(cols)*uint64(rows+snapshotScrollback) > maxScreenCells {
		return errScreenDimensions
	}
	return nil
}

func newTerminalScreen(cols, rows uint) (*terminalScreen, error) {
	if err := validateScreenDimensions(cols, rows); err != nil {
		return nil, fmt.Errorf("%w: %dx%d", err, cols, rows)
	}
	t := xterm.New(
		xterm.WithCols(int(cols)),
		xterm.WithRows(int(rows)),
		xterm.WithScrollback(snapshotScrollback),
	)
	return &terminalScreen{
		term:               t,
		addon:              xterm.NewSerializeAddon(t),
		cols:               cols,
		rows:               rows,
		continuationParser: xterm.NewEscapeSequenceParser(),
	}, nil
}

func (s *terminalScreen) write(p []byte) {
	if s == nil || s.term == nil || len(p) == 0 {
		return
	}
	_, _ = s.term.Write(p)
	s.trackContinuation(p)
}
func isCSIContinuationState(state xterm.ParserState) bool {
	switch state {
	case xterm.ParserStateCSIEntry, xterm.ParserStateCSIParam,
		xterm.ParserStateCSIIntermediate, xterm.ParserStateCSIIgnore:
		return true
	default:
		return false
	}
}

func appendCSIValue(out []byte, value int32) []byte {
	return strconv.AppendInt(out, int64(value), 10)
}

func canonicalCSI(raw []byte, state xterm.ParserState) ([]byte, bool) {
	const (
		esc = byte('\x1b')
	)
	start := 0
	if len(raw) >= 2 && raw[0] == esc && raw[1] == '[' {
		start = 2
	} else if len(raw) >= 2 && raw[0] == 0xc2 && raw[1] == 0x9b {
		start = 2
	} else {
		return nil, false
	}

	out := make([]byte, 0, 128)
	out = append(out, esc, '[')
	if state == xterm.ParserStateCSIIgnore {
		return append(out, '?', '?'), true
	}

	var prefix byte
	if start < len(raw) && raw[start] >= '<' && raw[start] <= '?' {
		prefix = raw[start]
		start++
	}
	if prefix != 0 {
		out = append(out, prefix)
	}

	var params [32]int32
	var subParams [32][32]int32
	var subCount [32]uint8
	var subTotal uint8
	paramCount := 1
	rejectDigits := false
	digitIsSub := false
	rejectSubDigits := false
	hasParams := false
	var intermediates [8]byte
	intermediateCount := 0

	for _, b := range raw[start:] {
		switch {
		case b >= '0' && b <= '9':
			hasParams = true
			if rejectDigits {
				continue
			}
			if digitIsSub {
				if rejectSubDigits || subCount[paramCount-1] == 0 {
					continue
				}
				index := subCount[paramCount-1] - 1
				value := subParams[paramCount-1][index]
				if value == -1 {
					value = int32(b - '0')
				} else {
					value = value*10 + int32(b-'0')
				}
				subParams[paramCount-1][index] = value
				continue
			}
			value := params[paramCount-1]
			value = value*10 + int32(b-'0')
			params[paramCount-1] = value
		case b == ';':
			hasParams = true
			digitIsSub = false
			if paramCount >= len(params) {
				rejectDigits = true
				continue
			}
			params[paramCount] = 0
			paramCount++
		case b == ':':
			hasParams = true
			digitIsSub = true
			if rejectDigits || subTotal >= uint8(len(subParams)) {
				rejectSubDigits = true
				continue
			}
			index := subCount[paramCount-1]
			subParams[paramCount-1][index] = -1
			subCount[paramCount-1]++
			subTotal++
		case b >= 0x20 && b <= 0x2f:
			if intermediateCount < len(intermediates) {
				intermediates[intermediateCount] = b
				intermediateCount++
			} else {
				copy(intermediates[:], intermediates[1:])
				intermediates[len(intermediates)-1] = b
			}
		}

	}

	if hasParams {
		for param := range paramCount {
			if param > 0 {
				out = append(out, ';')
			}
			out = appendCSIValue(out, params[param])
			for sub := range subCount[param] {
				out = append(out, ':')
				if subParams[param][sub] != -1 {
					out = appendCSIValue(out, subParams[param][sub])
				}
			}
		}
	}
	if rejectDigits {
		out = append(out, ';')
	} else if rejectSubDigits {
		out = append(out, ':')
	}
	return append(out, intermediates[:intermediateCount]...), true
}

func (s *terminalScreen) rebuildContinuationParser() {
	s.continuationParser.Reset()
	for _, b := range s.continuation {
		s.parserCodepoint[0] = uint32(b)
		s.continuationParser.Parse(s.parserCodepoint[:], 1)
	}
}

func (s *terminalScreen) appendContinuationBytes(p []byte) {
	if s.continuationOverflow || len(p) == 0 {
		return
	}
	state := s.continuationParser.CurrentState()
	if isCSIContinuationState(state) && len(s.continuation)+len(p) > maxCSIContinuation {
		raw := make([]byte, len(s.continuation)+len(p))
		copy(raw, s.continuation)
		copy(raw[len(s.continuation):], p)
		if canonical, ok := canonicalCSI(raw, state); ok {
			s.continuation = canonical
			s.rebuildContinuationParser()
			return
		}
	}
	if len(s.continuation)+len(p) > maxSnapshotContinuation {
		s.continuation = nil
		s.continuationOverflow = true
		s.continuationState = state
		return
	}
	s.continuation = append(s.continuation, p...)
}

func utf8SequenceLength(b byte) int {
	switch {
	case b&0xe0 == 0xc0:
		return 2
	case b&0xf0 == 0xe0:
		return 3
	case b&0xf8 == 0xf0:
		return 4
	default:
		return 0
	}
}

func (s *terminalScreen) trackCodepoint(codepoint uint32, raw []byte) {
	if s.continuationParser == nil {
		return
	}
	before := s.continuationParser.CurrentState()
	s.parserCodepoint[0] = codepoint
	s.continuationParser.Parse(s.parserCodepoint[:], 1)
	after := s.continuationParser.CurrentState()

	if before != xterm.ParserStateGround || after != xterm.ParserStateGround {
		s.appendContinuationBytes(raw)
	}
	if after == xterm.ParserStateGround {
		s.continuation = nil
		s.continuationOverflow = false
		s.continuationState = xterm.ParserStateGround
	}
}

func continuationCandidate(p []byte) int {
	for i := 0; i < len(p); i++ {
		if p[i] == '\x1b' {
			return i
		}
		if p[i]&0x80 == 0 {
			continue
		}
		if !utf8.FullRune(p[i:]) {
			return i
		}
		codepoint, size := utf8.DecodeRune(p[i:])
		if codepoint >= 0x80 && codepoint <= 0x9f {
			return i
		}
		i += size - 1
	}
	return -1
}

func (s *terminalScreen) trackContinuation(p []byte) {
	for i := 0; i < len(p); {
		if s.utf8PendingLen == 0 && s.continuationParser.CurrentState() == xterm.ParserStateGround {
			candidate := continuationCandidate(p[i:])
			if candidate < 0 {
				return
			}
			i += candidate
		}
		b := p[i]
		if s.utf8PendingLen == 0 {
			if b < 0x80 {
				s.trackCodepoint(uint32(b), p[i:i+1])
				i++
				continue
			}
			need := utf8SequenceLength(b)
			if need == 0 {
				// Match xterm-go's decoder: invalid start bytes are ignored.
				i++
				continue
			}
			s.utf8Pending[0] = b
			s.utf8PendingLen = 1
			s.utf8PendingNeed = need
			i++
			continue
		}

		if b&0xc0 != 0x80 {
			// The decoder discards an incomplete sequence and retries this
			// byte as a possible new codepoint.
			s.utf8PendingLen = 0
			s.utf8PendingNeed = 0
			continue
		}
		s.utf8Pending[s.utf8PendingLen] = b
		s.utf8PendingLen++
		i++
		if s.utf8PendingLen < s.utf8PendingNeed {
			continue
		}

		raw := s.utf8Pending[:s.utf8PendingLen]
		codepoint, size := utf8.DecodeRune(raw)
		s.utf8PendingLen = 0
		s.utf8PendingNeed = 0
		if codepoint == utf8.RuneError && size <= 1 {
			// xterm-go skips malformed sequences and does not feed them to
			// the VT parser.
			continue
		}
		if codepoint == '\ufeff' || (codepoint >= 0xd800 && codepoint <= 0xdfff) {
			continue
		}
		s.trackCodepoint(uint32(codepoint), raw)
	}
}

func (s *terminalScreen) resize(cols, rows uint) error {
	if s == nil || s.term == nil {
		return nil
	}
	if err := validateScreenDimensions(cols, rows); err != nil {
		return fmt.Errorf("%w: %dx%d", err, cols, rows)
	}
	s.term.Resize(int(cols), int(rows))
	s.cols, s.rows = cols, rows
	return nil
}

func (s *terminalScreen) dispose() {
	if s == nil || s.term == nil {
		return
	}
	s.term.Dispose()
	s.term = nil
	s.addon = nil
}

// snapshot returns the serialized state without the reset prefix. Callers
// prepend a reset because dashboard terminals may retain an earlier screen.
func (s *terminalScreen) snapshot() []byte {
	if s == nil || s.term == nil || s.addon == nil {
		return nil
	}
	return s.addon.Serialize(nil)
}

// Modes and DECSTBM home the cursor after SerializeAddon has positioned it.
func (s *terminalScreen) appendSnapshotCursor(data []byte) []byte {
	origin := s.term.DecPrivateModes().Origin
	if !origin && s.term.ScrollTop() == 0 && s.term.ScrollBottom() == s.term.Rows()-1 {
		return data
	}
	row, col := s.term.CursorY(), s.term.CursorX()
	if origin {
		row -= s.term.ScrollTop()
	}
	pendingWrap := col >= s.term.Cols()
	var cell xterm.CellData
	if pendingWrap {
		col = s.term.Cols() - 1
		line := s.term.Buffer().Lines.Get(s.term.Buffer().YBase + s.term.CursorY())
		line.LoadCell(col, &cell)
		if cell.GetWidth() == 0 && col > 0 {
			col--
			line.LoadCell(col, &cell)
		}
	}
	data = fmt.Appendf(data, "\x1b[%d;%dH", row+1, col+1)
	if pendingWrap {
		// CUP cannot restore the wrap-pending column. Repaint the last glyph
		// with its own attributes, then restore the attributes for new output.
		data = appendScreenAttributes(data, cell.AttributeData)
		data = append(data, cell.GetChars()...)
		data = appendScreenAttributes(data, s.term.CurAttrData())
	}
	return data
}

func appendScreenAttributes(data []byte, attr xterm.AttributeData) []byte {
	data = append(data, "\x1b[0"...)
	for _, flag := range [...]struct {
		set  uint32
		code string
	}{
		{attr.IsBold(), ";1"}, {attr.IsDim(), ";2"},
		{attr.IsItalic(), ";3"}, {attr.IsBlink(), ";5"},
		{attr.IsInverse(), ";7"}, {attr.IsInvisible(), ";8"},
		{attr.IsStrikethrough(), ";9"}, {attr.IsOverline(), ";53"},
	} {
		if flag.set != 0 {
			data = append(data, flag.code...)
		}
	}
	if attr.IsUnderline() != 0 {
		data = fmt.Appendf(data, ";4:%d", attr.GetUnderlineStyle())
	}
	for _, color := range [...]struct {
		mode  uint32
		value int
		code  int
	}{
		{attr.GetFgColorMode(), attr.GetFgColor(), 38},
		{attr.GetBgColorMode(), attr.GetBgColor(), 48},
		{attr.GetUnderlineColorMode(), attr.GetUnderlineColor(), 58},
	} {
		switch color.mode {
		case xterm.AttrCMP16:
			if color.code == 58 {
				data = fmt.Appendf(data, ";58:5:%d", color.value)
			} else if color.value < 8 {
				data = fmt.Appendf(data, ";%d", color.code-8+color.value)
			} else {
				data = fmt.Appendf(data, ";%d", color.code+44+color.value)
			}
		case xterm.AttrCMP256:
			data = fmt.Appendf(data, ";%d:5:%d", color.code, color.value)
		case xterm.AttrCMRGB:
			data = fmt.Appendf(data, ";%d:2::%d:%d:%d", color.code,
				(color.value>>16)&255, (color.value>>8)&255, color.value&255)
		}
	}
	data = append(data, 'm')
	if attr.IsProtected() != 0 {
		return append(data, "\x1b[1\"q"...)
	}
	return append(data, "\x1b[0\"q"...)
}

func (s *terminalScreen) overflowContinuation() []byte {
	switch s.continuationState {
	case xterm.ParserStateOSCString:
		// Enter OSC's abort state. The dependency's string handlers discard
		// payloads that exceed ParserPayloadLimit, so future bytes must be
		// consumed without invoking a handler or reaching the viewport.
		return []byte{'\x1b', ']', 'x'}
	case xterm.ParserStateSOSPMString:
		return []byte{'\x1b', 'X'}
	case xterm.ParserStateDCSEntry, xterm.ParserStateDCSParam,
		xterm.ParserStateDCSIgnore, xterm.ParserStateDCSIntermediate,
		xterm.ParserStateDCSPassthrough:
		// A pair of private parameter bytes takes DCS into its ignore state.
		return []byte{'\x1b', 'P', '?', '?'}
	case xterm.ParserStateAPCEntry, xterm.ParserStateAPCIntermediate,
		xterm.ParserStateAPCPassthrough:
		return []byte{'\x1b', '_', '?'}
	case xterm.ParserStateCSIEntry, xterm.ParserStateCSIParam,
		xterm.ParserStateCSIIntermediate, xterm.ParserStateCSIIgnore:
		return []byte{'\x1b', '[', '?', '?'}
	case xterm.ParserStateEscape, xterm.ParserStateEscapeIntermediate:
		return []byte{'\x1b'}
	default:
		return nil
	}
}

func (s *terminalScreen) appendPendingContinuation(data []byte) []byte {
	if s.continuationOverflow {
		return append(data, s.overflowContinuation()...)
	}
	data = append(data, s.continuation...)
	return append(data, s.utf8Pending[:s.utf8PendingLen]...)
}

func makeScreenSnapshot(screen *terminalScreen, modes modeScanner) ScreenSnapshot {
	if screen == nil || screen.term == nil {
		return ScreenSnapshot{}
	}
	serialized := screen.snapshot()
	preamble := modes.preamble()
	data := make([]byte, 0, 2+len(serialized)+len(preamble)+len(screen.continuation)+screen.utf8PendingLen+160)
	data = append(data, '\x1b', 'c')
	data = append(data, serialized...)
	data = screen.appendSnapshotCursor(data)
	// Mode restoration must be complete before an unfinished sequence is
	// replayed; otherwise its bytes can be interpreted as part of a mode
	// preamble or cancel the parser state we are handing off.
	data = append(data, preamble...)
	data = screen.appendPendingContinuation(data)
	return ScreenSnapshot{Cols: screen.cols, Rows: screen.rows, Data: data}
}

func cloneScreenSnapshot(in ScreenSnapshot) ScreenSnapshot {
	in.Data = append([]byte(nil), in.Data...)
	return in
}

type recordedScreen struct {
	screen *terminalScreen
	modes  modeScanner
}

// readCastScreen reconstructs the terminal and tracked modes in one pass over
// a cast. It deliberately consumes resize events before later output, unlike
// replayReader which is for raw output only. A truncated final JSON event is
// ignored so a crash during the last write does not erase a usable screen;
// malformed complete events still fail recovery.
func readCastScreen(path string) (recordedScreen, error) {
	f, err := os.Open(path)
	if err != nil {
		return recordedScreen{}, err
	}
	defer func() { _ = f.Close() }()

	r := bufio.NewReader(f)
	line, readErr := r.ReadBytes('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return recordedScreen{}, fmt.Errorf("ptyhost: read transcript header: %w", readErr)
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return recordedScreen{}, errors.New("ptyhost: transcript has no header")
	}
	var header castHeader
	if err = json.Unmarshal(line, &header); err != nil {
		return recordedScreen{}, fmt.Errorf("ptyhost: decode transcript header: %w", err)
	}
	if header.Version != 2 {
		return recordedScreen{}, fmt.Errorf("ptyhost: unsupported transcript version %d", header.Version)
	}
	screen, err := newTerminalScreen(header.Width, header.Height)
	if err != nil {
		return recordedScreen{}, fmt.Errorf("ptyhost: restore transcript screen: %w", err)
	}
	var modes modeScanner
	for {
		line, readErr = r.ReadBytes('\n')
		atEnd := errors.Is(readErr, io.EOF)
		if readErr != nil && !atEnd {
			screen.dispose()
			return recordedScreen{}, fmt.Errorf("ptyhost: read transcript: %w", readErr)
		}
		line = bytes.TrimSpace(line)
		if len(line) != 0 {
			var event []json.RawMessage
			if uerr := json.Unmarshal(line, &event); uerr != nil || len(event) < 3 {
				if atEnd && isIncompleteJSON(uerr) {
					break
				}
				screen.dispose()
				if uerr == nil {
					uerr = errBadCastString
				}
				return recordedScreen{}, fmt.Errorf("ptyhost: decode transcript event: %w", uerr)
			}
			var code string
			if uerr := json.Unmarshal(event[1], &code); uerr != nil {
				screen.dispose()
				return recordedScreen{}, fmt.Errorf("ptyhost: decode transcript event code: %w", uerr)
			}
			if err := applyRecordedEvent(screen, &modes, code, event[2]); err != nil {
				screen.dispose()
				return recordedScreen{}, err
			}
		}
		if atEnd {
			break
		}
	}
	return recordedScreen{screen: screen, modes: modes}, nil
}

func isIncompleteJSON(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "unexpected end of JSON input")
}

func applyRecordedEvent(screen *terminalScreen, modes *modeScanner, code string, raw json.RawMessage) error {
	switch code {
	case "o":
		data, err := decodeCastString(raw)
		if err != nil {
			return fmt.Errorf("ptyhost: decode transcript output: %w", err)
		}
		screen.write(data)
		modes.scan(data)
	case "r":
		var geometry string
		if err := json.Unmarshal(raw, &geometry); err != nil {
			return fmt.Errorf("ptyhost: decode transcript resize: %w", err)
		}
		parts := strings.Split(geometry, "x")
		if len(parts) != 2 {
			return fmt.Errorf("ptyhost: malformed transcript resize %q", geometry)
		}
		cols, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil {
			return fmt.Errorf("ptyhost: malformed transcript resize %q: %w", geometry, err)
		}
		rows, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			return fmt.Errorf("ptyhost: malformed transcript resize %q: %w", geometry, err)
		}
		if err := screen.resize(uint(cols), uint(rows)); err != nil {
			return fmt.Errorf("ptyhost: restore transcript resize: %w", err)
		}
	}
	return nil
}
