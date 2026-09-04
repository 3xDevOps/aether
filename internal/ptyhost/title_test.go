package ptyhost

import (
	"strings"
	"testing"
)

func TestTitleScanner(t *testing.T) {
	var got []string
	scanner := &titleScanner{}
	report := func(title string) { got = append(got, title) }

	scanner.scan([]byte("\x1b]0;Fix"), report)
	scanner.scan([]byte("ing the login bug\x07"), report)
	scanner.scan([]byte("\x1b]2;Building\x1b\\"), report)

	long := strings.Repeat("界", 121)
	scanner.scan([]byte("\x1b]0;"+long+"\x07"), report)
	scanner.scan([]byte("\x1b]0;a\x01b\x7f\x07"), report)
	scanner.scan([]byte("\x1b]0;ab\x07"), report)

	want := []string{
		"Fixing the login bug",
		"Building",
		strings.Repeat("界", 120),
		"ab",
	}
	if len(got) != len(want) {
		t.Fatalf("reported titles = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reported title %d = %q, want %q", i, got[i], want[i])
		}
	}
}
