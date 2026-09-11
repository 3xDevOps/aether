package ptyhost

import (
	"bytes"
	"path/filepath"
	"testing"
)

func scanned(chunks ...string) *modeScanner {
	var s modeScanner
	for _, c := range chunks {
		s.scan([]byte(c))
	}
	return &s
}

func TestModePreambleRestoresWhatTheAgentSet(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{
			name:   "untouched terminal needs no preamble",
			chunks: []string{"plain output\r\n"},
			want:   "",
		},
		{
			name:   "bracketed paste survives the output that follows it",
			chunks: []string{"\x1b[?2004h", "a year of scrollback\r\n"},
			want:   "\x1b[?2004h",
		},
		{
			name:   "the last write of a mode wins",
			chunks: []string{"\x1b[?2004h", "\x1b[?2004l"},
			want:   "",
		},
		{
			name:   "one sequence can carry several modes",
			chunks: []string{"\x1b[?1002;1006h"},
			want:   "\x1b[?1002;1006h",
		},
		{
			name:   "the cursor is shown by default, so hiding it is what reports",
			chunks: []string{"\x1b[?25l"},
			want:   "\x1b[?25l",
		},
		{
			name:   "autowrap is on by default, so turning it off is what reports",
			chunks: []string{"\x1b[?7l"},
			want:   "\x1b[?7l",
		},
		{
			name:   "autowrap restored to its default reports nothing",
			chunks: []string{"\x1b[?7l", "\x1b[?7h"},
			want:   "",
		},
		{
			name:   "set and reset modes are reported in one pass each",
			chunks: []string{"\x1b[?2004h", "\x1b[?25l"},
			want:   "\x1b[?2004h\x1b[?25l",
		},
		{
			name:   "an escape split across reads is still recognised",
			chunks: []string{"\x1b[?20", "04", "h"},
			want:   "\x1b[?2004h",
		},
		{
			name:   "untracked private modes are not asserted for the agent",
			chunks: []string{"\x1b[?1049h\x1b[?12h"},
			want:   "",
		},
		{
			name:   "the public form of a mode is not the private one",
			chunks: []string{"\x1b[4h"},
			want:   "",
		},
		{
			name:   "a private sequence that is not a mode set is ignored",
			chunks: []string{"\x1b[?1000$p"},
			want:   "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scanned(tc.chunks...).preamble()
			if string(got) != tc.want {
				t.Fatalf("preamble = %q, want %q", got, tc.want)
			}
		})
	}
}

// The reason the scanner exists: the bytes that set a mode leave the replay
// ring long before a member reattaches, and a dashboard terminal resets
// itself before every replay.
func TestModePreambleOutlivesTheReplayRing(t *testing.T) {
	tr, err := newCastWriter(filepath.Join(t.TempDir(), "cast"), 80, 24)
	if err != nil {
		t.Fatalf("newCastWriter: %v", err)
	}
	t.Cleanup(func() { _ = tr.close() })
	s := &session{ring: newRing(1024), tr: tr, clients: map[*client]struct{}{}}
	s.deliver([]byte("\x1b[?2004h"))
	s.deliver(bytes.Repeat([]byte("scrollback\r\n"), 200))

	if bytes.Contains(s.ring.bytes(), []byte("\x1b[?2004h")) {
		t.Fatal("ring still holds the mode sequence; the test no longer covers a wrapped ring")
	}
	c := newClient(nil, AttachClient{Cols: 80, Rows: 24})
	if aerr := s.addClient(c); aerr != nil {
		t.Fatalf("addClient: %v", aerr)
	}
	if !bytes.HasPrefix(c.replay, []byte("\x1b[?2004h")) {
		t.Fatalf("replay does not restore bracketed paste: %q", head(c.replay))
	}
}

func head(p []byte) []byte {
	if len(p) > 32 {
		return p[:32]
	}
	return p
}

// A lone watcher sizes the PTY the way ssh does, and stops the moment
// anyone else is there to have their screen reflowed.
func TestSoloMirrorImposesUntilCompany(t *testing.T) {
	s := &session{clients: map[*client]struct{}{}, cols: 120, rows: 30}
	mirror := newClient(nil, AttachClient{Cols: 80, Rows: 24, ReadOnly: true})
	s.clients[mirror] = struct{}{}
	if !s.imposesNow(mirror) {
		t.Fatal("a lone mirror should size the session")
	}

	phone := newClient(nil, AttachClient{Cols: 40, Rows: 20, ReadOnly: true, Follow: true})
	s.clients[phone] = struct{}{}
	if !s.imposesNow(mirror) {
		t.Fatal("a follower is not company: it renders at whatever size it is told")
	}
	if s.imposesNow(phone) {
		t.Fatal("a follower must never size the session")
	}

	other := newClient(nil, AttachClient{Cols: 200, Rows: 60, ReadOnly: true})
	s.clients[other] = struct{}{}
	if s.imposesNow(mirror) || s.imposesNow(other) {
		t.Fatal("two mirrors reflow each other, so neither may size the session")
	}

	writer := newClient(nil, AttachClient{Cols: 100, Rows: 40})
	s.clients[writer] = struct{}{}
	if !s.imposesNow(writer) {
		t.Fatal("a client that can write always sizes the session")
	}
}

// Taking control is a change of state, not a redraw: the client keeps the
// screen and the terminal state it already has.
func TestResumeKeepsTheScreen(t *testing.T) {
	tr, err := newCastWriter(filepath.Join(t.TempDir(), "cast"), 80, 24)
	if err != nil {
		t.Fatalf("newCastWriter: %v", err)
	}
	t.Cleanup(func() { _ = tr.close() })
	s := &session{ring: newRing(1024), tr: tr, clients: map[*client]struct{}{}, cols: 80, rows: 24}
	s.deliver([]byte("\x1b[?2004h agent output\r\n"))

	resuming := newClient(nil, AttachClient{Cols: 80, Rows: 24, Resume: true})
	if aerr := s.addClient(resuming); aerr != nil {
		t.Fatalf("addClient: %v", aerr)
	}
	if len(resuming.replay) != 0 {
		t.Fatalf("resume replayed %q, want nothing", resuming.replay)
	}
	if s.geoGen != 0 {
		t.Fatalf("resume at an unchanged size scheduled a redraw (geoGen = %d)", s.geoGen)
	}

	fresh := newClient(nil, AttachClient{Cols: 80, Rows: 24})
	if aerr := s.addClient(fresh); aerr != nil {
		t.Fatalf("addClient: %v", aerr)
	}
	if len(fresh.replay) == 0 {
		t.Fatal("a client that is not resuming must still get the replay")
	}
}

// A resume that brings a different size is still a resize: the client kept
// its screen, but the PTY has to be told what that screen now is.
func TestResumeAtANewSizeStillResizes(t *testing.T) {
	tr, err := newCastWriter(filepath.Join(t.TempDir(), "cast"), 80, 24)
	if err != nil {
		t.Fatalf("newCastWriter: %v", err)
	}
	t.Cleanup(func() { _ = tr.close() })
	s := &session{ring: newRing(1024), tr: tr, clients: map[*client]struct{}{}, cols: 80, rows: 24}
	c := newClient(nil, AttachClient{Cols: 132, Rows: 43, Resume: true})
	if aerr := s.addClient(c); aerr != nil {
		t.Fatalf("addClient: %v", aerr)
	}
	if s.cols != 132 || s.rows != 43 {
		t.Fatalf("session geometry = %dx%d, want 132x43", s.cols, s.rows)
	}
}
