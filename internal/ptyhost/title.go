package ptyhost

import (
	"unicode"
	"unicode/utf8"
)

const maxTitleRunes = 120

type titleState uint8

const (
	titleNormal titleState = iota
	titleEsc
	titleCode
	titleSemi
	titleText
	titleTextEsc
)

// titleScanner extracts OSC 0 and OSC 2 titles without consuming other PTY
// output. Its state survives chunks because PTY reads can split an escape.
type titleScanner struct {
	state   titleState
	text    []rune
	pending []byte
	last    string
	hasLast bool
}

// scan reports each newly observed title. A nil report still advances the
// scanner and suppresses duplicate titles on later calls.
func (s *titleScanner) scan(p []byte, report func(string)) {
	for _, b := range p {
		s.byte(b, report)
	}
}

func (s *titleScanner) byte(b byte, report func(string)) {
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
		case '0', '2':
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
			s.finish(report)
		case 0x1b:
			s.state = titleTextEsc
		default:
			s.appendByte(b)
		}
	case titleTextEsc:
		if b == '\\' {
			s.finish(report)
			return
		}
		s.state = titleText
		if b == '\a' {
			s.finish(report)
			return
		}
		s.appendByte(b)
	}
}

func (s *titleScanner) appendByte(b byte) {
	if len(s.text) >= maxTitleRunes {
		return
	}
	s.pending = append(s.pending, b)
	for len(s.pending) > 0 {
		r, size := utf8.DecodeRune(s.pending)
		if r == utf8.RuneError && size == 1 && !utf8.FullRune(s.pending) {
			return
		}
		s.pending = s.pending[size:]
		if r != utf8.RuneError && !unicode.IsControl(r) && len(s.text) < maxTitleRunes {
			s.text = append(s.text, r)
		}
		if len(s.text) == maxTitleRunes {
			s.pending = nil
			return
		}
	}
}

func (s *titleScanner) finish(report func(string)) {
	title := string(s.text)
	if !s.hasLast || title != s.last {
		s.last, s.hasLast = title, true
		if report != nil {
			report(title)
		}
	}
	s.state = titleNormal
	s.text = s.text[:0]
	s.pending = s.pending[:0]
}
