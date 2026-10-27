package preference

import (
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

// MinWords and MinChars bound the shortest preference: a shorter one would
// match too much of what the member types or launches.
const (
	MinWords = 3
	MinChars = 12
)

// Word is one run of letters and digits of a text, case-folded, with its
// byte offsets in that text.
type Word struct {
	Text       string
	Start, End int
}

// Words splits text into its words, ignoring punctuation and case.
func Words(text string) []Word {
	fold := cases.Fold()
	var out []Word
	start := -1
	for i, r := range text + " " {
		word := unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
		switch {
		case word && start < 0:
			start = i
		case !word && start >= 0:
			out = append(out, Word{Text: fold.String(text[start:i]), Start: start, End: i})
			start = -1
		}
	}
	return out
}

// Index returns where words holds want as a contiguous run, or -1.
func Index(words, want []Word) int {
	for i := 0; len(want) > 0 && i+len(want) <= len(words); i++ {
		match := true
		for j := range want {
			if words[i+j].Text != want[j].Text {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// tooShort reports whether normalized text is shorter than a preference may
// be.
func tooShort(text string) bool {
	return len(Words(text)) < MinWords || utf8.RuneCountInString(text) < MinChars
}
