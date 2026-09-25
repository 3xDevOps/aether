package ptyhost

import (
	"context"
	"errors"
	"strings"
	"time"

	xterm "github.com/gitpod-io/xterm-go"
)

// MaxOutputBytes and MaxWait bound individual observation requests.
const MaxOutputBytes = 1 << 20
const MaxWait = 30 * time.Second

// ScreenColor preserves default, indexed, and RGB colors without choosing a viewer theme.
type ScreenColor struct {
	Mode string `json:"mode"`
	Value int `json:"value"`
}

type ScreenCell struct {
	Text string `json:"text"`
	Width int `json:"width"`
	Foreground ScreenColor `json:"foreground"`
	Background ScreenColor `json:"background"`
	UnderlineColor ScreenColor `json:"underline_color"`
	Bold bool `json:"bold"`
	Dim bool `json:"dim"`
	Italic bool `json:"italic"`
	Blink bool `json:"blink"`
	Inverse bool `json:"inverse"`
	Invisible bool `json:"invisible"`
	Strikethrough bool `json:"strikethrough"`
	Overline bool `json:"overline"`
	Underline int `json:"underline"`
	Protected bool `json:"protected"`
}

type ScreenLine struct {
	Text string `json:"text"`
	Wrapped bool `json:"wrapped"`
	Cells []ScreenCell `json:"cells"`
}

// Cursor coordinates are zero-based. X equal to Cols means wrap is pending.
type ScreenCursor struct {
	X int `json:"x"`
	Y int `json:"y"`
	Visible bool `json:"visible"`
}

type TerminalModes struct {
	ApplicationCursorKeys bool `json:"application_cursor_keys"`
	ApplicationKeypad bool `json:"application_keypad"`
	BracketedPaste bool `json:"bracketed_paste"`
	MouseTracking string `json:"mouse_tracking"`
	MouseEncoding string `json:"mouse_encoding"`
	SendFocus bool `json:"send_focus"`
}

// ScreenObservation captures the active viewport, not a transcript tail. Every
// field, including the replayable VT snapshot, is read at one session boundary.
type ScreenObservation struct {
	Generation uint64 `json:"generation"`
	Incarnation string `json:"incarnation"`
	Position TerminalPosition `json:"position"`
	Revision uint64 `json:"revision"`
	GeometryRevision uint64 `json:"geometry_revision"`
	Cols uint `json:"cols"`
	Rows uint `json:"rows"`
	Text string `json:"text"`
	Lines []ScreenLine `json:"lines"`
	Cursor ScreenCursor `json:"cursor"`
	Alternate bool `json:"alternate"`
	Modes TerminalModes `json:"modes"`
	Snapshot ScreenSnapshot `json:"snapshot"`
	Ended bool `json:"ended"`
	ProtocolError string `json:"protocol_error,omitempty"`
}

func screenColor(mode uint32, value int) ScreenColor {
	switch mode {
	case xterm.AttrCMP16, xterm.AttrCMP256:
		return ScreenColor{Mode: "indexed", Value: value}
	case xterm.AttrCMRGB:
		return ScreenColor{Mode: "rgb", Value: value}
	default:
		return ScreenColor{Mode: "default", Value: -1}
	}
}

func (s *session) observationLocked() (ScreenObservation, error) {
	if s.stopped {
		return ScreenObservation{}, ErrNoSession
	}
	if s.screen == nil || s.screen.term == nil {
		return ScreenObservation{}, ErrSnapshotUnavailable
	}
	t := s.screen.term
	m := t.DecPrivateModes()
	o := ScreenObservation{
		Generation: s.generation, Incarnation: s.resumeID,
		Position: s.currentPositionLocked(), Revision: s.revision,
		GeometryRevision: s.geometryRevision, Cols: s.screen.cols, Rows: s.screen.rows,
		Cursor: ScreenCursor{X: t.CursorX(), Y: t.CursorY(), Visible: !t.IsCursorHidden()},
		Alternate: t.IsAltBufferActive(), Ended: s.ended,
		Modes: TerminalModes{ApplicationCursorKeys: m.ApplicationCursorKeys,
			ApplicationKeypad: m.ApplicationKeypad, BracketedPaste: m.BracketedPasteMode,
			MouseTracking: m.MouseTrackingMode, MouseEncoding: m.MouseEncoding, SendFocus: m.SendFocus},
		Snapshot: s.screenSnapshotLocked(), Lines: make([]ScreenLine, t.Rows()),
	}
	if s.protocolErr != nil {
		o.ProtocolError = s.protocolErr.Error()
	}
	buf := t.Buffer()
	cells := make([]ScreenCell, t.Rows()*t.Cols())
	var text strings.Builder
	var cell xterm.CellData
	for y := range o.Lines {
		line := buf.Lines.Get(buf.YBase+y)
		row := &o.Lines[y]
		row.Cells = cells[y*t.Cols():(y+1)*t.Cols()]
		if line != nil {
			row.Text = line.TranslateToString(true, 0, t.Cols())
			row.Wrapped = line.IsWrapped
			for x := range row.Cells {
				line.LoadCell(x, &cell)
				row.Cells[x] = ScreenCell{Text: cell.GetChars(), Width: cell.GetWidth(),
					Foreground: screenColor(cell.GetFgColorMode(), cell.GetFgColor()),
					Background: screenColor(cell.GetBgColorMode(), cell.GetBgColor()),
					UnderlineColor: screenColor(cell.GetUnderlineColorMode(), cell.GetUnderlineColor()),
					Bold: cell.IsBold()!=0, Dim: cell.IsDim()!=0, Italic: cell.IsItalic()!=0,
					Blink: cell.IsBlink()!=0, Inverse: cell.IsInverse()!=0, Invisible: cell.IsInvisible()!=0,
					Strikethrough: cell.IsStrikethrough()!=0, Overline: cell.IsOverline()!=0,
					Underline: int(cell.GetUnderlineStyle()), Protected: cell.IsProtected()!=0}
			}
		}
		if y != 0 { text.WriteByte('\n') }
		text.WriteString(row.Text)
	}
	o.Text = text.String()
	return o, nil
}

func (h *Host) ObserveSession(key SessionKey) (ScreenObservation, error) {
	s := h.lookup(key)
	if s == nil { return ScreenObservation{}, ErrNoSession }
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observationLocked()
}

type OutputRequest struct {
	After *TerminalPosition
	MaxBytes int
}

type OutputObservation struct {
	Generation uint64 `json:"generation"`
	Incarnation string `json:"incarnation"`
	Start TerminalPosition `json:"start"`
	Next TerminalPosition `json:"next"`
	Position TerminalPosition `json:"position"`
	Data []byte `json:"data"`
	MissingCursor bool `json:"missing_cursor"`
	Truncated bool `json:"truncated"`
	More bool `json:"more"`
	Ended bool `json:"ended"`
}

// outputBoundaryLocked distinguishes an absent/wrong/future cursor from an
// evicted one. Both restart at the oldest retained byte, explicitly marked.
func (s *session) outputBoundaryLocked(after *TerminalPosition) (start TerminalPosition, missing, truncated bool) {
	end := s.currentPositionLocked()
	start = end
	if s.ring != nil { start.Sequence -= TerminalSequence(len(s.ring.buf)) }
	if after == nil || after.Epoch != end.Epoch || after.Sequence > end.Sequence {
		return start, true, start.Sequence != 0
	}
	if after.Sequence < start.Sequence { return start, false, true }
	return *after, false, false
}

func (h *Host) ReadSessionOutput(key SessionKey, request OutputRequest) (OutputObservation, error) {
	if request.MaxBytes < 0 || request.MaxBytes > MaxOutputBytes { return OutputObservation{}, errors.New("ptyhost: output limit out of bounds") }
	if request.MaxBytes == 0 { request.MaxBytes = 64 << 10 }
	s := h.lookup(key)
	if s == nil { return OutputObservation{}, ErrNoSession }
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped { return OutputObservation{}, ErrNoSession }
	start, missing, truncated := s.outputBoundaryLocked(request.After)
	o := OutputObservation{Generation: s.generation, Incarnation: s.resumeID,
		Start: start, Next: start, Position: s.currentPositionLocked(), MissingCursor: missing,
		Truncated: truncated, Ended: s.ended}
	if s.ring != nil {
		offset := len(s.ring.buf)-int(o.Position.Sequence-start.Sequence)
		n := min(request.MaxBytes, len(s.ring.buf)-offset)
		o.Data = append([]byte(nil), s.ring.buf[offset:offset+n]...)
		o.Next.Sequence += TerminalSequence(n)
	}
	o.More = o.Next.Sequence < o.Position.Sequence
	return o, nil
}

type WaitRequest struct {
	Generation uint64
	AfterRevision uint64
	AfterOutput *TerminalPosition
	Contains string
	Ended bool
	Timeout time.Duration
}

type WaitObservation struct {
	Screen ScreenObservation `json:"screen"`
	Matched bool `json:"matched"`
	TimedOut bool `json:"timed_out"`
	MissingCursor bool `json:"missing_cursor"`
	Truncated bool `json:"truncated"`
}

// WaitSession waits for any requested condition. Timeout is an observation,
// never a match. Cancellation returns ctx.Err; replacement never retargets.
func (h *Host) WaitSession(ctx context.Context, key SessionKey, request WaitRequest) (WaitObservation, error) {
	if request.Generation == 0 || request.Timeout < 0 || request.Timeout > MaxWait {
		return WaitObservation{}, errors.New("ptyhost: invalid wait generation or timeout")
	}
	if request.AfterRevision == 0 && request.AfterOutput == nil && request.Contains == "" && !request.Ended {
		return WaitObservation{}, errors.New("ptyhost: wait condition required")
	}
	if request.Timeout == 0 { request.Timeout = MaxWait }
	s := h.lookup(key)
	if s == nil { return WaitObservation{}, ErrNoSession }
	timer := time.NewTimer(request.Timeout)
	defer timer.Stop()
	timedOut := false
	for {
		if err := ctx.Err(); err != nil { return WaitObservation{}, err }
		if h.lookup(key) != s || s.generation != request.Generation { return WaitObservation{}, ErrSessionReplaced }
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return WaitObservation{}, ErrSessionReplaced
		}
		_, missing, truncated := s.outputBoundaryLocked(request.AfterOutput)
		outputMatch := request.AfterOutput != nil && (missing || truncated || s.currentPositionLocked() != *request.AfterOutput)
		matched := (request.AfterRevision != 0 && s.revision != request.AfterRevision) || outputMatch || (request.Ended && s.ended)
		if request.Contains != "" && s.screen != nil {
			matched = matched || strings.Contains(s.visibleTextLocked(), request.Contains)
		}
		if matched || timedOut || s.ended {
			o, err := s.observationLocked()
			s.mu.Unlock()
			return WaitObservation{Screen: o, Matched: matched, TimedOut: timedOut && !matched,
				MissingCursor: request.AfterOutput != nil && missing, Truncated: request.AfterOutput != nil && truncated}, err
		}
		if s.changed == nil { s.changed = make(chan struct{}) }
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done(): return WaitObservation{}, ctx.Err()
		case <-timer.C: timedOut = true
		case <-changed:
		}
	}
}

func (s *session) visibleTextLocked() string {
	t := s.screen.term
	buf := t.Buffer()
	var text strings.Builder
	for y := range t.Rows() {
		if y != 0 { text.WriteByte('\n') }
		text.WriteString(buf.TranslateBufferLineToString(buf.YBase+y, true, 0, t.Cols()))
	}
	return text.String()
}

func (s *session) changedLocked() {
	s.revision++
	if s.changed != nil { close(s.changed); s.changed = nil }
}
