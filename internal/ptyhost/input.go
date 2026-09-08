package ptyhost

// A write attach carries more than keystrokes. A TUI that asks the
// terminal a question gets the answer back on the same channel, a pane
// reports focus changes on its own, and once the TUI turns mouse tracking
// on, every click, drag and wheel notch is reported there too. None of
// that is a person steering the run, so none of it may make them a
// co-author of its commits.
//
// The rule is therefore inverted: rather than list the reports a terminal
// might send - a list that is never finished, and whose gaps credit the
// wrong person - this recognises what a keyboard can produce and treats
// everything else as the terminal talking. Concretely, a keyboard never
// emits a CSI whose parameters open with a private marker (< = > ?), a
// CSI carrying an intermediate byte (0x20 to 0x2f), a DCS, SOS, PM or APC
// string, or an OSC string. Those cover DSR and DECRPM replies, DECRQSS,
// XTVERSION, XTGETTCAP, the kitty keyboard query, the colour reads and
// SGR mouse reports alike.
//
// One key is lost to this: CSI 1;2R is Shift-F3 on some terminals and a
// cursor position report everywhere, and nothing in the byte stream tells
// them apart. It stays uncounted, which is the safe direction - failing
// to credit a steerer costs a trailer, crediting someone who only looked
// puts their name in history that gets merged.
//
// Every byte a client sends still reaches the PTY. What is decided here
// is only whether a write counts as someone having typed.

const (
	esc = 0x1b
	bel = 0x07
	// maxPartialReport bounds the tail carried between reads. A report
	// this does not finish within that many bytes is not one of these, and
	// counts as typing.
	maxPartialReport = 256
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
// part-way through one and the rest is still to come. A lone ESC is a key
// - it is how a TUI is interrupted - so it is never treated as the start
// of something still arriving.
func terminalReport(p []byte) (n int, truncated bool) {
	if p[0] != esc || len(p) == 1 {
		return 0, false
	}
	switch p[1] {
	case '[':
		return csiReport(p)
	case ']':
		// OSC. A keyboard cannot produce one; a terminal answers colour
		// and clipboard queries with them.
		return stringReport(p, 2)
	case 'P', 'X', '^', '_':
		// DCS, SOS, PM and APC. DECRQSS, XTVERSION and XTGETTCAP replies
		// all arrive as DCS strings.
		return stringReport(p, 2)
	}
	return 0, false
}

// csiReport measures a CSI a keyboard could not have produced. The
// function keys, the arrows, the editing keys, kitty key events and
// modifyOtherKeys all arrive as CSI too, so what is excluded is the shape
// a keyboard never takes: a private parameter marker, an intermediate
// byte, the legacy mouse report, and the handful of plain finals that are
// only ever answers.
func csiReport(p []byte) (int, bool) {
	i := 2
	// Legacy X10 mouse: CSI M and three raw bytes, which are not
	// parameters and may be any value at all.
	if i < len(p) && p[i] == 'M' {
		if len(p) < 6 {
			return 0, true
		}
		return 6, false
	}
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
	if p[i] < 0x40 || p[i] > 0x7e {
		return 0, false
	}
	switch {
	case end > params && p[params] >= '<' && p[params] <= '?':
		// A private marker opens every SGR mouse report (CSI < ...) and
		// every DECRPM and kitty reply (CSI ? ...).
		return i + 1, false
	case i > end:
		// An intermediate byte: DECRQM answers are CSI ... $ y.
		return i + 1, false
	}
	switch p[i] {
	case 'I', 'O', 'c', 'R', 't', 'n':
		// Focus in and out, device attributes, cursor position, window
		// size, and device status.
		return i + 1, false
	case '~':
		if s := string(p[params:end]); s == "200" || s == "201" {
			// The bracketed-paste markers wrap a paste and carry nothing
			// themselves; whatever they wrap is counted on its own.
			return i + 1, false
		}
	}
	return 0, false
}

// stringReport measures a string-terminated sequence - OSC, DCS, SOS, PM
// or APC - whose body runs to BEL or ST. Its content is never inspected:
// none of these can come from a keyboard whatever they carry.
func stringReport(p []byte, i int) (int, bool) {
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
