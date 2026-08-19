// Package attribution is the single home for member attribution colors:
// the canonical palette, least-used color assignment, hex validation,
// and the terminal render helpers every surface shares. It is pure data
// and string formatting - no policy, no I/O.
package attribution

import (
	"fmt"
	"strconv"
	"strings"
)

// Palette is the colorblind-safe palette members draw their attribution
// color from. Order matters: it is the tie-break for NextColor.
var Palette = []string{
	"#e6194b", "#3cb44b", "#ffe119", "#4363d8",
	"#f58231", "#911eb4", "#46f0f0", "#f032e6",
}

// NextColor picks the least-used palette color among taken (case-
// insensitive hex compare), breaking ties by palette index. Colors in
// taken that are not in the palette are ignored. Unlike indexing by
// member count, this stays balanced after member churn.
func NextColor(taken []string) string {
	counts := make(map[string]int, len(Palette))
	for _, t := range taken {
		counts[strings.ToLower(strings.TrimSpace(t))]++
	}
	best := 0
	for i, c := range Palette[1:] {
		if counts[c] < counts[Palette[best]] {
			best = i + 1
		}
	}
	return Palette[best]
}

// Normalize validates a #RRGGBB hex color (leading # optional) and
// returns it lowercased with the leading #.
func Normalize(hex string) (string, error) {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) != 6 {
		return "", fmt.Errorf("attribution: color %q is not #RRGGBB", hex)
	}
	for _, r := range h {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return "", fmt.Errorf("attribution: color %q is not #RRGGBB", hex)
		}
	}
	return "#" + strings.ToLower(h), nil
}

// ANSI returns the truecolor SGR prefix for a #RRGGBB hex color, or ""
// when hex is empty or invalid (callers can splice it in unconditionally).
func ANSI(hex string) string {
	h := strings.TrimPrefix(hex, "#")
	if len(h) != 6 {
		return ""
	}
	r, err1 := strconv.ParseUint(h[0:2], 16, 8)
	g, err2 := strconv.ParseUint(h[2:4], 16, 8)
	b, err3 := strconv.ParseUint(h[4:6], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return ""
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

// Sprint renders text in the given color with a trailing reset,
// passing text through unchanged when hex is empty or invalid.
func Sprint(hex, text string) string {
	seq := ANSI(hex)
	if seq == "" {
		return text
	}
	return seq + text + "\x1b[0m"
}
