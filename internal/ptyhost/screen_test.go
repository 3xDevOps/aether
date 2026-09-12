package ptyhost

import (
	"bytes"
	"testing"

	xterm "github.com/gitpod-io/xterm-go"
)

func assertEquivalentScreen(t *testing.T, want, got *xterm.Terminal) {
	t.Helper()
	if got.String() != want.String() {
		t.Fatalf("screen differs\n got: %q\nwant: %q", got.String(), want.String())
	}
	if got.CursorX() != want.CursorX() || got.CursorY() != want.CursorY() {
		t.Fatalf("cursor differs: got (%d,%d), want (%d,%d)", got.CursorX(), got.CursorY(), want.CursorX(), want.CursorY())
	}
	if got.ScrollTop() != want.ScrollTop() || got.ScrollBottom() != want.ScrollBottom() {
		t.Fatalf("scroll region differs: got %d;%d, want %d;%d", got.ScrollTop(), got.ScrollBottom(), want.ScrollTop(), want.ScrollBottom())
	}
	if got.Modes() != want.Modes() {
		t.Fatalf("ANSI modes differ: got %#v, want %#v", got.Modes(), want.Modes())
	}
	gotPrivate, wantPrivate := got.DecPrivateModes(), want.DecPrivateModes()
	if gotPrivate.Origin != wantPrivate.Origin || gotPrivate.Wraparound != wantPrivate.Wraparound ||
		gotPrivate.BracketedPasteMode != wantPrivate.BracketedPasteMode ||
		gotPrivate.ApplicationCursorKeys != wantPrivate.ApplicationCursorKeys {
		t.Fatalf("DEC modes differ: got %#v, want %#v", gotPrivate, wantPrivate)
	}

	for row := range want.Rows() {
		wantLine := want.Buffer().Lines.Get(want.Buffer().YBase + row)
		gotLine := got.Buffer().Lines.Get(got.Buffer().YBase + row)
		if wantLine == nil || gotLine == nil {
			if wantLine != gotLine {
				t.Fatalf("row %d presence differs", row)
			}
			continue
		}
		for col := range want.Cols() {
			wantCell, gotCell := xterm.NewCellData(), xterm.NewCellData()
			wantLine.LoadCell(col, wantCell)
			gotLine.LoadCell(col, gotCell)
			if wantCell.GetChars() != gotCell.GetChars() || wantCell.GetWidth() != gotCell.GetWidth() ||
				!wantCell.AttributesEqual(gotCell) {
				t.Fatalf("cell (%d,%d) differs: got %#v, want %#v", col, row, gotCell, wantCell)
			}
		}
	}
}

func restoreScreenSnapshot(t *testing.T, snapshot ScreenSnapshot) *xterm.Terminal {
	t.Helper()
	restored := xterm.New(
		xterm.WithCols(int(snapshot.Cols)),
		xterm.WithRows(int(snapshot.Rows)),
		xterm.WithScrollback(snapshotScrollback),
	)
	t.Cleanup(restored.Dispose)
	if _, err := restored.Write(snapshot.Data); err != nil {
		t.Fatal(err)
	}
	return restored
}

func TestTerminalScreenSnapshotKeepsCurrentViewportBounded(t *testing.T) {
	screen, err := newTerminalScreen(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()

	for range 40000 {
		screen.write([]byte("\x1b[2K\rredraw "))
	}
	screen.write([]byte("CURRENT-SNAPSHOT-PROMPT"))

	snapshot := makeScreenSnapshot(screen, modeScanner{})
	assertEquivalentScreen(t, screen.term, restoreScreenSnapshot(t, snapshot))
	if len(snapshot.Data) > 256<<10 {
		t.Fatalf("snapshot grew with redraw history: %d bytes", len(snapshot.Data))
	}
}
func TestTerminalScreenSnapshotContinuationMatchesUninterruptedScreen(t *testing.T) {
	screen, err := newTerminalScreen(40, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()

	prefix := []byte("\x1b[?2004h\x1b[?1049h\x1b[2J\x1b[H\x1b[38;2;20;180;40m日本語\x1b[0m\r\n\x1b[1;34mSURVIVES\x1b[0m")
	screen.write(prefix)
	var modes modeScanner
	snapshot := makeScreenSnapshot(screen, modes)
	continuation := []byte("\r\x1b[Ktyped continuation")
	screen.write(continuation)
	restored := restoreScreenSnapshot(t, snapshot)
	if _, err := restored.Write(continuation); err != nil {
		t.Fatal(err)
	}
	assertEquivalentScreen(t, screen.term, restored)
}

func TestTerminalScreenSnapshotPreservesSplitSequences(t *testing.T) {
	tests := []struct {
		name         string
		prefix       []byte
		continuation []byte
	}{
		{
			name:         "CSI",
			prefix:       []byte("\x1b["),
			continuation: []byte("2J\x1b[Hvisible"),
		},
		{
			name:         "UTF8",
			prefix:       []byte{0xe6, 0x97},
			continuation: []byte{0xa5, 'V', 'I', 'S', 'I', 'B', 'L', 'E'},
		},
		{
			name:         "malformed UTF8 before CSI",
			prefix:       []byte{0xe2, '\x1b', '['},
			continuation: []byte("2J\x1b[Hvisible"),
		},
		{
			name:         "OSC",
			prefix:       []byte("\x1b]2;split title"),
			continuation: []byte(" remains hidden\x07visible"),
		},
		{
			name:         "DCS",
			prefix:       []byte("\x1bP1;2+qsplit payload"),
			continuation: []byte(" remains hidden\x1b\\visible"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			screen, err := newTerminalScreen(32, 6)
			if err != nil {
				t.Fatal(err)
			}
			defer screen.dispose()

			screen.write(tc.prefix)
			snapshot := makeScreenSnapshot(screen, modeScanner{})
			screen.write(tc.continuation)
			restored := restoreScreenSnapshot(t, snapshot)
			if _, err := restored.Write(tc.continuation); err != nil {
				t.Fatal(err)
			}
			assertEquivalentScreen(t, screen.term, restored)
		})
	}
}

func TestTerminalScreenSnapshotModesPrecedePendingSequence(t *testing.T) {
	screen, err := newTerminalScreen(24, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()

	var modes modeScanner
	screen.write([]byte("\x1b[?2004h"))
	modes.scan([]byte("\x1b[?2004h"))
	screen.write([]byte("\x1b["))
	snapshot := makeScreenSnapshot(screen, modes)

	continuation := []byte("2J\x1b[Hvisible")
	screen.write(continuation)
	restored := restoreScreenSnapshot(t, snapshot)
	if _, err := restored.Write(continuation); err != nil {
		t.Fatal(err)
	}
	assertEquivalentScreen(t, screen.term, restored)
}

func TestTerminalScreenSnapshotPreservesScrollRegionAndModes(t *testing.T) {
	screen, err := newTerminalScreen(24, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()

	screen.write([]byte("\x1b[3;6r\x1b[?6h\x1b[?7l\x1b[4;3H\x1b[1;31mMARGIN\x1b[0m"))
	snapshot := makeScreenSnapshot(screen, modeScanner{})
	restored := restoreScreenSnapshot(t, snapshot)
	assertEquivalentScreen(t, screen.term, restored)

	continuation := []byte("012345678901234567890123\nTAIL")
	screen.write(continuation)
	if _, err := restored.Write(continuation); err != nil {
		t.Fatal(err)
	}
	assertEquivalentScreen(t, screen.term, restored)
}

func TestTerminalScreenSnapshotPreservesPendingWrap(t *testing.T) {
	for _, prefix := range []string{
		"\x1b[2;1H",
		"\x1b[3;6r\x1b[?6h\x1b[2;1H",
	} {
		screen, err := newTerminalScreen(24, 8)
		if err != nil {
			t.Fatal(err)
		}
		screen.write([]byte(prefix + "\x1b[1;31mabcdefghijklmnopqrstuv日\x1b[32m"))
		restored := restoreScreenSnapshot(t, makeScreenSnapshot(screen, modeScanner{}))
		assertEquivalentScreen(t, screen.term, restored)
		screen.write([]byte("X"))
		if _, err := restored.Write([]byte("X")); err != nil {
			t.Fatal(err)
		}
		assertEquivalentScreen(t, screen.term, restored)
		screen.dispose()
	}
}

func TestTerminalScreenSnapshotCanonicalizesLongCSIHeader(t *testing.T) {
	screen, err := newTerminalScreen(24, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.dispose()

	prefix := append([]byte("\x1b["), bytes.Repeat([]byte("9"), maxCSIContinuation+32)...)
	screen.write(prefix)
	snapshot := makeScreenSnapshot(screen, modeScanner{})
	if len(snapshot.Data) >= len(prefix) {
		t.Fatalf("long CSI header was not compacted: %d bytes", len(snapshot.Data))
	}

	continuation := []byte("Jvisible")
	screen.write(continuation)
	restored := restoreScreenSnapshot(t, snapshot)
	if _, err := restored.Write(continuation); err != nil {
		t.Fatal(err)
	}
	assertEquivalentScreen(t, screen.term, restored)
}

func TestTerminalScreenRejectsUnboundedDimensions(t *testing.T) {
	if _, err := newTerminalScreen(maxScreenCols, maxScreenRows); err == nil {
		t.Fatal("expected oversized cell allocation to fail")
	}
	if _, err := newTerminalScreen(0, 24); err == nil {
		t.Fatal("expected zero columns to fail")
	}
	if err := validateScreenDimensions(120, 30); err != nil {
		t.Fatalf("normal terminal dimensions rejected: %v", err)
	}
}
