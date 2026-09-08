package ptyhost

import (
	"strings"
	"testing"
)

// Everything a terminal emits without anyone touching a key. The CSI cases
// come from the xterm.js bundle the dashboard ships; the DCS ones from
// what a real terminal answers on the aether attach path, where the
// scrollback replay re-asks whatever the agent's TUI asked. A member who
// only caused these has not steered the run.
func TestTerminalReportsAreNotTyping(t *testing.T) {
	reports := map[string]string{
		"focus in":              "\x1b[I",
		"focus out":             "\x1b[O",
		"device attributes":     "\x1b[?1;2c",
		"secondary DA":          "\x1b[>0;276;0c",
		"cursor position":       "\x1b[24;80R",
		"device status ok":      "\x1b[0n",
		"printer status":        "\x1b[?10n",
		"text area size":        "\x1b[4;600;800t",
		"window size chars":     "\x1b[8;30;120t",
		"DECRPM":                "\x1b[?2026;2$y",
		"DECRQSS reply":         "\x1bP1$r0m\x1b\\",
		"XTVERSION reply":       "\x1bP>|xterm(390)\x1b\\",
		"XTGETTCAP reply":       "\x1bP1+r5455=5C45\x1b\\",
		"kitty keyboard flags":  "\x1b[?0u",
		"SGR mouse press":       "\x1b[<0;10;5M",
		"SGR mouse release":     "\x1b[<0;10;5m",
		"SGR mouse wheel up":    "\x1b[<64;10;5M",
		"SGR mouse wheel down":  "\x1b[<65;10;5M",
		"SGR mouse motion":      "\x1b[<35;11;6M",
		"legacy mouse":          "\x1b[M\x20\x30\x30",
		"legacy mouse high bit": "\x1b[M\x60\xc8\xc8",
		"foreground colour":     "\x1b]10;rgb:ffff/ffff/ffff\x07",
		"background colour":     "\x1b]11;rgb:0000/0000/0000\x1b\\",
		"cursor colour":         "\x1b]12;rgb:ffff/ffff/ffff\x07",
		"palette colour":        "\x1b]4;1;rgb:cd00/0000/0000\x07",
		"clipboard reply":       "\x1b]52;c;aGk=\x07",
		"APC string":            "\x1b_Gi=1;OK\x1b\\",
		"empty paste":           "\x1b[200~\x1b[201~",
		"focus out then in":     "\x1b[O\x1b[I",
		"wheel over a run":      "\x1b[<64;1;1M\x1b[<64;1;1M\x1b[<64;1;1M",
		"reports back to back":  "\x1b[I\x1b[?1;2c\x1b]11;rgb:0000/0000/0000\x07\x1b[<64;9;9M\x1b[O",
	}
	for name, seq := range reports {
		t.Run(name, func(t *testing.T) {
			var sc inputScanner
			if sc.typed([]byte(seq)) {
				t.Errorf("%q counted as typing", seq)
			}
		})
	}
}

// Everything a person does counts, including the keys that arrive as CSI
// sequences of their own and a paste, whose markers are skipped but whose
// contents are not.
func TestKeystrokesAreTyping(t *testing.T) {
	strokes := map[string]string{
		"letter":             "a",
		"carriage return":    "\r",
		"control c":          "\x03",
		"escape alone":       "\x1b",
		"escape then letter": "\x1ba",
		"up arrow":           "\x1b[A",
		"home":               "\x1b[H",
		"delete":             "\x1b[3~",
		"function key":       "\x1b[15~",
		"shift tab":          "\x1b[Z",
		"SS3 function key":   "\x1bOP",
		"ctrl arrow":         "\x1b[1;5A",
		"kitty key event":    "\x1b[97;5u",
		"modifyOtherKeys":    "\x1b[27;5;97~",
		"paste":              "\x1b[200~git commit\x1b[201~",
		"key after reports":  "\x1b[I\x1b[?1;2cy",
		"key before report":  "y\x1b[O",
		"key after a wheel":  "\x1b[<64;1;1My",
	}
	for name, seq := range strokes {
		t.Run(name, func(t *testing.T) {
			var sc inputScanner
			if !sc.typed([]byte(seq)) {
				t.Errorf("%q did not count as typing", seq)
			}
		})
	}
}

// A report can arrive in two writes. Joining them is what keeps the halves
// from being read as input a person produced.
func TestReportSplitAcrossWrites(t *testing.T) {
	for _, seq := range []string{
		"\x1b]11;rgb:0000/0000/0000\x07",
		"\x1bP>|xterm(390)\x1b\\",
		"\x1b[<64;10;5M",
		"\x1b[?2026;2$y",
	} {
		// From two bytes on: a bare ESC is a key in its own right, so a
		// split that isolates it is counted, which is the cost of not
		// swallowing the key that interrupts a TUI.
		for split := 2; split < len(seq); split++ {
			var sc inputScanner
			if sc.typed([]byte(seq[:split])) {
				t.Fatalf("split %d: first half of %q counted as typing", split, seq)
			}
			if sc.typed([]byte(seq[split:])) {
				t.Errorf("split %d: rejoined %q counted as typing", split, seq)
			}
		}
	}
	// The join must not swallow a keystroke that follows the report.
	var sc inputScanner
	if sc.typed([]byte("\x1b[")) {
		t.Fatal("a bare CSI introducer counted as typing")
	}
	if !sc.typed([]byte("Ia")) {
		t.Error("a key behind a rejoined focus report did not count as typing")
	}
}

// An unterminated sequence longer than any report this knows is input,
// not a report still arriving: the carry cannot grow without bound.
func TestOverlongPartialCountsAsTyping(t *testing.T) {
	var sc inputScanner
	if !sc.typed([]byte("\x1b]11;" + strings.Repeat("x", maxPartialReport))) {
		t.Error("an unterminated sequence past the carry limit did not count as typing")
	}
}
