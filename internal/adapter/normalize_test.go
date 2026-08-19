package adapter

import (
	"reflect"
	"testing"
)

func feedAll(n *LineNormalizer, chunks ...string) []string {
	var lines []string
	for _, c := range chunks {
		lines = append(lines, n.Feed([]byte(c))...)
	}
	return lines
}

func TestNormalizerLines(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"lf", []string{"a\nb\n"}, []string{"a", "b"}},
		{"crlf", []string{"a\r\nb\r\n"}, []string{"a", "b"}},
		{"bare cr terminates", []string{"frame1\rframe2\n"}, []string{"frame1", "frame2"}},
		{"empty lines suppressed", []string{"\n\r\n\r\ra\n"}, []string{"a"}},
		{"csi stripped", []string{"\x1b[32mgreen\x1b[0m\n"}, []string{"green"}},
		{"private csi stripped", []string{"\x1b[?25ltext\x1b[?25h\n"}, []string{"text"}},
		{"osc bel stripped", []string{"\x1b]0;title\x07text\n"}, []string{"text"}},
		{"osc st stripped", []string{"\x1b]0;title\x1b\\text\n"}, []string{"text"}},
		{"bare esc sequence stripped", []string{"\x1b(Btext\n"}, []string{"text"}},
		{"c0 dropped tab kept", []string{"a\x08b\tc\n"}, []string{"ab\tc"}},
		{"line split across feeds", []string{"par", "tial\n"}, []string{"partial"}},
		{"escape split across feeds", []string{"a\x1b[", "32mb\n"}, []string{"ab"}},
		{"osc split across feeds", []string{"\x1b]0;ti", "tle\x07x\n"}, []string{"x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var n LineNormalizer
			got := feedAll(&n, tc.chunks...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("lines = %q, want %q", got, tc.want)
			}
			if line, ok := n.Flush(); ok {
				t.Errorf("unexpected trailing partial line %q", line)
			}
		})
	}
}

func TestNormalizerFlush(t *testing.T) {
	var n LineNormalizer
	if got := n.Feed([]byte("no terminator")); len(got) != 0 {
		t.Fatalf("Feed returned %q before any terminator", got)
	}
	line, ok := n.Flush()
	if !ok || line != "no terminator" {
		t.Fatalf("Flush = %q, %v; want %q, true", line, ok, "no terminator")
	}
	if _, ok := n.Flush(); ok {
		t.Error("second Flush reported a line")
	}
}

// TestNormalizerOversizedLine: a multi-MiB line without a terminator never
// grows the pending buffer past the cap, the oversized line is dropped as
// opaque, and the next normal line still parses.
func TestNormalizerOversizedLine(t *testing.T) {
	var n LineNormalizer
	chunk := make([]byte, 64*1024)
	for i := range chunk {
		chunk[i] = 'x'
	}
	// 4 MiB of one endless line, fed in chunks.
	for range 4 << 20 / len(chunk) {
		if got := n.Feed(chunk); len(got) != 0 {
			t.Fatalf("oversized line emitted %d lines mid-stream", len(got))
		}
		if len(n.buf) > maxLineBytes {
			t.Fatalf("pending buffer grew to %d, cap %d", len(n.buf), maxLineBytes)
		}
	}
	// Terminate the monster line, then send a normal one: only the
	// normal line comes out.
	got := n.Feed([]byte("\nnext line\n"))
	if len(got) != 1 || got[0] != "next line" {
		t.Fatalf("after overflow got %q, want [%q]", got, "next line")
	}
	if _, ok := n.Flush(); ok {
		t.Error("Flush reported a line after clean terminator")
	}

	// An oversized line at EOF is dropped by Flush too, and the state
	// resets for reuse.
	var n2 LineNormalizer
	for range maxLineBytes/len(chunk) + 2 {
		n2.Feed(chunk)
	}
	if line, ok := n2.Flush(); ok {
		t.Errorf("Flush returned %d-byte oversized line, want none", len(line))
	}
	if got := n2.Feed([]byte("clean\n")); len(got) != 1 || got[0] != "clean" {
		t.Errorf("after overflow flush got %q, want [%q]", got, "clean")
	}
}
