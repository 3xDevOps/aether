package ptyhost

import (
	"unicode"
	"unicode/utf8"
)

const maxTitleRunes = 120
const maxSemanticTitleRunes = 1024

type titleState uint8

const (
	titleNormal titleState = iota
	titleEsc
	titleCode
	titleSemi
	titleText
	titleTextEsc
)

// titleScanner extracts OSC 0, OSC 1, and OSC 2 titles without consuming other
// PTY output. Its state survives chunks because PTY reads can split an escape.
type titleScanner struct {
	state      titleState
	text       []rune
	pending    []byte
	tailOffset int
	last       string
	hasLast    bool
}

// scan reports each newly observed title. A nil report still advances the
// scanner and suppresses duplicate titles on later calls.
func (s *titleScanner) scan(p []byte, report func(string)) {
	s.scanWithObserver(p, nil, report)
}

// scanWithObserver reports every complete title to observe, while report keeps
// the historical deduplicated display callback behavior.
func (s *titleScanner) scanWithObserver(p []byte, observe, report func(string)) {
	for _, b := range p {
		s.byte(b, observe, report)
	}
}

func (s *titleScanner) byte(b byte, observe, report func(string)) {

	switch s.state {
	case titleNormal:
		if b == 0x1b {
			s.state = titleEsc
		}
	case titleEsc:
		switch b {
		case ']':
			s.state = titleCode
		case 0x1b:
			s.state = titleEsc
		default:
			s.state = titleNormal
		}
	case titleCode:
		switch b {
		case '0', '1', '2':
			s.state = titleSemi
		case 0x1b:
			s.state = titleEsc
		default:
			s.state = titleNormal
		}
	case titleSemi:
		switch b {
		case ';':
			s.state = titleText
		case 0x1b:
			s.state = titleEsc
		default:
			s.state = titleNormal
		}
	case titleText:
		switch b {
		case '\a':
			s.finish(observe, report)
		case 0x1b:
			s.state = titleTextEsc
		default:
			s.appendByte(b)
		}
	case titleTextEsc:
		if b == '\\' {
			s.finish(observe, report)
			return
		}
		s.state = titleText
		if b == '\a' {
			s.finish(observe, report)
			return
		}
		s.appendByte(b)
	}
}

func (s *titleScanner) appendByte(b byte) {
	s.pending = append(s.pending, b)
	for len(s.pending) > 0 {
		r, size := utf8.DecodeRune(s.pending)
		if r == utf8.RuneError && size == 1 && !utf8.FullRune(s.pending) {
			return
		}
		s.pending = s.pending[size:]
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		if len(s.text) < maxSemanticTitleRunes {
			s.text = append(s.text, r)
		} else {
			// Keep both protocol prefixes and trailing status words, like Orca.
			const half = maxSemanticTitleRunes / 2
			s.text[half+s.tailOffset] = r
			s.tailOffset = (s.tailOffset + 1) % half
		}
	}
}

func (s *titleScanner) finish(observe, report func(string)) {
	title := string(s.text[:min(len(s.text), maxTitleRunes)])
	if observe != nil {
		semantic := s.text
		if s.tailOffset != 0 {
			const half = maxSemanticTitleRunes / 2
			semantic = make([]rune, 0, maxSemanticTitleRunes)
			semantic = append(semantic, s.text[:half]...)
			semantic = append(semantic, s.text[half+s.tailOffset:]...)
			semantic = append(semantic, s.text[half:half+s.tailOffset]...)
		}
		observe(string(semantic))
	}
	if !s.hasLast || title != s.last {
		s.last, s.hasLast = title, true
		if report != nil {
			report(title)
		}
	}
	s.state = titleNormal
	s.text = s.text[:0]
	s.pending = s.pending[:0]
	s.tailOffset = 0
}
