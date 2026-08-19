package attribution

import (
	"strings"
	"testing"
)

func TestNextColor(t *testing.T) {
	tests := []struct {
		name  string
		taken []string
		want  string
	}{
		{"empty", nil, Palette[0]},
		{"skips taken", []string{Palette[0]}, Palette[1]},
		{"least used wins", []string{
			Palette[0], Palette[0],
			Palette[1], Palette[2], Palette[3],
			Palette[4], Palette[5], Palette[6], Palette[7],
		}, Palette[1]},
		{"tie-break by palette index", []string{Palette[0], Palette[1]}, Palette[2]},
		{"case-insensitive", []string{strings.ToUpper(Palette[0])}, Palette[1]},
		{"unknown colors ignored", []string{"#123456", "not-a-color"}, Palette[0]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NextColor(tt.taken); got != tt.want {
				t.Errorf("NextColor(%v) = %q, want %q", tt.taken, got, tt.want)
			}
		})
	}
}

func TestNextColorWraparound(t *testing.T) {
	// All palette colors taken once: assignment starts over at index 0.
	if got := NextColor(Palette); got != Palette[0] {
		t.Errorf("NextColor(all taken) = %q, want %q", got, Palette[0])
	}
	// Simulate churn: every color used once except index 3, freed by removal.
	taken := make([]string, 0, len(Palette)-1)
	for i, c := range Palette {
		if i != 3 {
			taken = append(taken, c)
		}
	}
	if got := NextColor(taken); got != Palette[3] {
		t.Errorf("NextColor(churned) = %q, want %q", got, Palette[3])
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"#e6194b", "#e6194b", true},
		{"E6194B", "#e6194b", true},
		{"#AbCdEf", "#abcdef", true},
		{" #3cb44b ", "#3cb44b", true},
		{"", "", false},
		{"#fff", "", false},
		{"#e6194b0", "", false},
		{"#gggggg", "", false},
		{"red", "", false},
		{"##e6194b", "", false},
	}
	for _, tt := range tests {
		got, err := Normalize(tt.in)
		if tt.ok != (err == nil) {
			t.Errorf("Normalize(%q) err = %v, want ok=%v", tt.in, err, tt.ok)
			continue
		}
		if got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestANSIAndSprint(t *testing.T) {
	if got := ANSI("#ff0000"); got != "\x1b[38;2;255;0;0m" {
		t.Errorf("ANSI(#ff0000) = %q", got)
	}
	for _, bad := range []string{"", "#fff", "zzzzzz", "#zzzzzz"} {
		if got := ANSI(bad); got != "" {
			t.Errorf("ANSI(%q) = %q, want empty", bad, got)
		}
		if got := Sprint(bad, "ada"); got != "ada" {
			t.Errorf("Sprint(%q) = %q, want passthrough", bad, got)
		}
	}
	want := "\x1b[38;2;255;0;0mada\x1b[0m"
	if got := Sprint("#ff0000", "ada"); got != want {
		t.Errorf("Sprint = %q, want %q", got, want)
	}
}
