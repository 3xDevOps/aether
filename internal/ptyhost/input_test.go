package ptyhost

import (
	"strings"
	"testing"
)

// The sequences a terminal produces without anyone touching a key, taken
// from the xterm.js bundle the dashboard ships: focus in and out, the DA1
// reply to CSI c, cursor position and window size reports, and the OSC
// colour replies. A member who only ever caused these has not steered the
// run and must not be credited as a co-author of its commits.
func TestTerminalReportsAreNotTyping(t *testing.T) {
	reports := map[string]string{
		"focus in":             "\x1b[I",
		"focus out":            "\x1b[O",
		"device attributes":    "\x1b[?1;2c",
		"secondary DA":         "\x1b[>0;276;0c",
		"cursor position":      "\x1b[24;80R",
		"text area size":       "\x1b[4;600;800t",
		"window size chars":    "\x1b[8;30;120t",
		"foreground colour":    "\x1b]10;rgb:ffff/ffff/ffff\x07",
		"background colour":    "\x1b]11;rgb:0000/0000/0000\x1b\\",
		"cursor colour":        "\x1b]12;rgb:ffff/ffff/ffff\x07",
		"palette colour":       "\x1b]4;1;rgb:cd00/0000/0000\x07",
		"empty paste":          "\x1b[200~\x1b[201~",
		"focus out then in":    "\x1b[O\x1b[I",
		"reports back to back": "\x1b[I\x1b[?1;2c\x1b]11;rgb:0000/0000/0000\x07\x1b[O",
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

// Everything a person actually does counts, including the keys that arrive
// as CSI sequences of their own and a paste, whose markers are skipped but
// whose contents are not.
func TestKeystrokesAreTyping(t *testing.T) {
	strokes := map[string]string{
		"letter":            "a",
		"carriage return":   "\r",
		"control c":         "\x03",
		"up arrow":          "\x1b[A",
		"delete":            "\x1b[3~",
		"shift tab":         "\x1b[Z",
		"function key":      "\x1bOP",
		"paste":             "\x1b[200~git commit\x1b[201~",
		"key after reports": "\x1b[I\x1b[?1;2cy",
		"key before report": "y\x1b[O",
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
	for _, split := range []int{1, 2, 3, 5} {
		seq := "\x1b]11;rgb:0000/0000/0000\x07"
		if split >= len(seq) {
			continue
		}
		var sc inputScanner
		if sc.typed([]byte(seq[:split])) {
			t.Fatalf("split %d: first half of %q counted as typing", split, seq)
		}
		if sc.typed([]byte(seq[split:])) {
			t.Errorf("split %d: rejoined %q counted as typing", split, seq)
		}
	}
	// The same join must not swallow a keystroke that follows the report.
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
