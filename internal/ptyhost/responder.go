package ptyhost

import (
	"context"
	"errors"
	"fmt"

	xterm "github.com/gitpod-io/xterm-go"
)

const maxProtocolReplyBytes = 64 << 10

// TerminalResponderWriter advertises live query ownership before attach replay.
// Development viewers must disable their query handlers, not keyboard/mouse
// input. Raw terminal input is deliberately never heuristically filtered.
type TerminalResponderWriter interface {
	SetTerminalResponder(serverOwned bool)
}

// All emulator callbacks run synchronously under session.mu. They enqueue only;
// a single worker uses the same serialized physical writer as admitted input.
func (s *session) enableProtocolResponder() {
	s.protocolWake = make(chan struct{}, 1)
	s.screen.palette = newTerminalPalette()
	t := s.screen.term
	t.OnData(s.queueProtocolReplyLocked)
	t.OnColor(func(events []xterm.ColorEvent) {
		for _, event := range events {
			if event.Index < 0 || event.Index >= len(s.screen.palette.colors) { continue }
			switch event.Type {
			case xterm.ColorRequestSet:
				if event.Color != nil {
					c := *event.Color
					s.screen.palette.colors[event.Index] = uint32(c[0])<<16 | uint32(c[1])<<8 | uint32(c[2])
				}
			case xterm.ColorRequestRestore:
				s.screen.palette.colors[event.Index] = defaultTerminalColor(event.Index)
			case xterm.ColorRequestReport:
				s.queueProtocolReplyLocked(string(appendColorReport(nil, event.Index, s.screen.palette.colors[event.Index])))
			}
		}
	})
	// OSC 104 with an empty payload means all palette entries. xterm-go's
	// ColorEvent conflates that request with restoring index zero, so handle
	// just the empty form before its default handler.
	t.RegisterOscHandler(104, xterm.NewOscStringHandler(func(data string) bool {
		if data != "" { return false }
		for i := range 256 { s.screen.palette.colors[i] = defaultTerminalColor(i) }
		return true
	}))
	// RIS also resets palette overrides. Return false to retain emulator reset.
	t.RegisterEscHandler(xterm.FunctionIdentifier{Final: 'c'}, func() bool {
		for i := range s.screen.palette.colors { s.screen.palette.colors[i] = defaultTerminalColor(i) }
		return false
	})
}

func (s *session) queueProtocolReplyLocked(data string) {
	if s.ended || s.stopped || s.protocolErr != nil { return }
	if len(data) > maxProtocolReplyBytes-len(s.protocolPending) {
		s.protocolErr = errors.New("ptyhost: terminal protocol response queue overflow")
	} else {
		s.protocolPending = append(s.protocolPending, data...)
	}
	select { case s.protocolWake <- struct{}{}: default: }
}

func (s *session) respond() {
	for {
		select {
		case <-s.done: return
		case <-s.protocolWake:
		}
		for {
			s.mu.Lock()
			failure := s.protocolErr
			data := s.protocolPending
			s.protocolPending = nil
			ended := s.ended || s.stopped
			s.mu.Unlock()
			if ended { return }
			if failure == nil && len(data) != 0 {
				failure = s.writeStdinContext(context.Background(), data)
			}
			if failure != nil {
				s.mu.Lock()
				s.protocolErr = failure
				att := s.att
				s.mu.Unlock()
				// A failed response lane is a failed attachment, not silent
				// success. Retain its final screen/error for observation.
				s.end()
				if att != nil { _ = att.Close() }
				return
			}
			if len(data) == 0 { break }
		}
	}
}

// A shared session has one stable xterm palette, independent of which viewer
// happens to be connected. Applications can change it with standard OSCs.
type terminalPalette struct { colors [259]uint32 }

func newTerminalPalette() *terminalPalette {
	p := &terminalPalette{}
	for i := range p.colors { p.colors[i] = defaultTerminalColor(i) }
	return p
}

func defaultTerminalColor(index int) uint32 {
	ansi := [...]uint32{0x000000, 0xcd0000, 0x00cd00, 0xcdcd00, 0x0000ee, 0xcd00cd, 0x00cdcd, 0xe5e5e5,
		0x7f7f7f, 0xff0000, 0x00ff00, 0xffff00, 0x5c5cff, 0xff00ff, 0x00ffff, 0xffffff}
	if index < 16 { return ansi[index] }
	if index < 232 {
		v := index-16
		levels := [...]uint32{0, 95, 135, 175, 215, 255}
		return levels[v/36]<<16 | levels[(v/6)%6]<<8 | levels[v%6]
	}
	if index < 256 {
		v := uint32(8+(index-232)*10)
		return v<<16 | v<<8 | v
	}
	if index == 257 { return 0x000000 }
	return 0xffffff
}

func appendColorReport(out []byte, index int, rgb uint32) []byte {
	if index < 256 { out = fmt.Appendf(out, "\x1b]4;%d;", index) } else { out = fmt.Appendf(out, "\x1b]%d;", index-246) }
	return fmt.Appendf(out, "rgb:%04x/%04x/%04x\x1b\\", ((rgb>>16)&255)*257, ((rgb>>8)&255)*257, (rgb&255)*257)
}

func (p *terminalPalette) appendSnapshot(out []byte) []byte {
	for i, color := range p.colors {
		// Restore all slots: a reconnecting viewer may have a prior palette.
		out = appendColorReport(out, i, color)
	}
	return out
}
