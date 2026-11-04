package dictation

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxHints    = 200
	maxHintSize = 80
	maxHintRun  = 4
)

func checkHints(hints []string) error {
	if len(hints) > maxHints {
		return fmt.Errorf("hints has %d entries; at most %d", len(hints), maxHints)
	}
	for i, h := range hints {
		if len(h) == 0 || len(h) > maxHintSize {
			return fmt.Errorf("hints[%d] is %d bytes; each hint is 1 to %d bytes", i, len(h), maxHintSize)
		}
	}
	return nil
}

// ApplyHints respells a run of one to four transcript words as a hint when
// the run's letters and digits, lowercased and joined, equal the hint's
// exactly: "login mounts" becomes "loginMounts". Punctuation before the
// run and after it stays. Nothing approximate is ever replaced, so a
// transcript can only gain the spellings the caller supplied.
func ApplyHints(text string, hints []string) string {
	byKey := map[string]string{}
	for _, h := range hints {
		if k := squash(h); k != "" {
			if _, dup := byKey[k]; !dup {
				byKey[k] = h
			}
		}
	}
	words := strings.Fields(text)
	if len(byKey) == 0 {
		return strings.Join(words, " ")
	}
	out := make([]string, 0, len(words))
	for i := 0; i < len(words); {
		n, hint := match(words[i:], byKey)
		if n == 0 {
			out = append(out, words[i])
			i++
			continue
		}
		lead, _, _ := splitPunct(words[i])
		_, _, trail := splitPunct(words[i+n-1])
		out = append(out, lead+hint+trail)
		i += n
	}
	return strings.Join(out, " ")
}

// match finds the longest run at the start of words that spells a hint.
// Only the run's outer edges may carry punctuation, so "sync, d" is two
// words, not "syncd".
func match(words []string, byKey map[string]string) (int, string) {
	var key strings.Builder
	best, hint := 0, ""
	for n := 1; n <= maxHintRun && n <= len(words); n++ {
		lead, core, trail := splitPunct(words[n-1])
		if core == "" || (n > 1 && lead != "") {
			break
		}
		key.WriteString(squash(core))
		if h, ok := byKey[key.String()]; ok {
			best, hint = n, h
		}
		if trail != "" {
			break
		}
	}
	return best, hint
}

func splitPunct(w string) (lead, core, trail string) {
	start := strings.IndexFunc(w, isWordRune)
	if start < 0 {
		return w, "", ""
	}
	end := strings.LastIndexFunc(w, isWordRune)
	_, size := utf8.DecodeRuneInString(w[end:])
	end += size
	return w[:start], w[start:end], w[end:]
}

func squash(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isWordRune(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
