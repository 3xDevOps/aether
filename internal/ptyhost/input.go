package ptyhost

// A write attach carries more than keystrokes. A TUI that asks the
// terminal a question gets the answer back on the same channel, and a
// browser pane reports focus changes on its own: xterm.js replies to
// device-attribute and colour queries and sends CSI I / CSI O when the
// pane gains or loses focus. None of that is a person steering the run,
// so none of it may make them a co-author of its commits.
//
// Every byte a client sends still reaches the PTY. What is decided here
// is only whether a write counts as someone having typed.

const (
	esc = 0x1b
	bel = 0x07
	// maxPartialReport bounds the tail carried between reads. The longest
	// report recognised here is an OSC colour reply of about thirty bytes;
	// anything longer is not one of these and counts as typing.
	maxPartialReport = 128
)

// inputScanner decides whether the writes of one attach include anything
// a person typed. Its only state is the tail of a report split across two
// reads, and one attach's read loop is its only caller.
type inputScanner struct{ partial []byte }

// typed reports whether p, joined to whatever the previous call left
// unfinished, holds a keystroke or a paste.
func (sc *inputScanner) typed(p []byte) bool {
	if len(sc.partial) > 0 {
		p = append(sc.partial, p...)
		sc.partial = nil
	}
	for len(p) > 0 {
		n, truncated := terminalReport(p)
		switch {
		case n > 0:
			p = p[n:]
		case truncated && len(p) <= maxPartialReport:
			sc.partial = append([]byte(nil), p...)
			return false
		default:
			return true
		}
	}
	return false
}

// terminalReport measures the terminal report at the front of p. It
// returns the report's length, or zero with truncated set when p stops
// part-way through one and the rest is still to come.
func terminalReport(p []byte) (n int, truncated bool) {
	if p[0] != esc {
		return 0, false
	}
	if len(p) == 1 {
		return 0, true
	}
	switch p[1] {
	case '[':
		return csiReport(p)
	case ']':
		return oscReport(p)
	}
	return 0, false
}

// csiReport matches the CSI sequences a terminal sends of its own accord:
// focus in and out (I, O), a device attributes reply (c), a cursor
// position report (R), a window size report (t), and the two
// bracketed-paste markers, which wrap a paste but carry nothing
// themselves. Every other final byte is a key - the arrows and the editing
// keys all arrive as CSI too.
func csiReport(p []byte) (int, bool) {
	i := 2
	params := i
	for i < len(p) && p[i] >= 0x30 && p[i] <= 0x3f {
		i++
	}
	end := i
	for i < len(p) && p[i] >= 0x20 && p[i] <= 0x2f {
		i++
	}
	if i == len(p) {
		return 0, true
	}
	switch p[i] {
	case 'I', 'O', 'c', 'R', 't':
		return i + 1, false
	case '~':
		if s := string(p[params:end]); s == "200" || s == "201" {
			return i + 1, false
		}
	}
	return 0, false
}

// oscReport matches the OSC replies a terminal answers a colour query
// with: the palette (4) and the foreground, background and cursor colours
// (10, 11, 12). The reply ends at BEL or at ST.
func oscReport(p []byte) (int, bool) {
	i := 2
	start := i
	for i < len(p) && p[i] >= '0' && p[i] <= '9' {
		i++
	}
	// The digits are checked only once they have all arrived: a lone "1"
	// at the end of a write is still on its way to being 10, 11 or 12.
	if i == len(p) {
		return 0, true
	}
	if i == start {
		return 0, false
	}
	switch string(p[start:i]) {
	case "4", "10", "11", "12":
	default:
		return 0, false
	}
	if p[i] != ';' {
		return 0, false
	}
	for ; i < len(p); i++ {
		switch p[i] {
		case bel:
			return i + 1, false
		case esc:
			if i+1 == len(p) {
				return 0, true
			}
			if p[i+1] == '\\' {
				return i + 2, false
			}
			return 0, false
		}
	}
	return 0, true
}
