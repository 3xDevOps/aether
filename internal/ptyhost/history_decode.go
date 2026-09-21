package ptyhost

import (
	"context"
	"strings"
	"unicode/utf8"
)

type historyDecoder struct {
	ctx    context.Context
	work   *historyWorkDeadline
	err    error
	onLine func(historyDecodedLine)

	cells          []rune
	column         int
	lineOrigin     *historyPosition
	lineTime       int64
	dirty          bool
	ansi           byte
	csi            [64]byte
	csiLen         int
	utf8Buf        []byte
	utf8Origin     historyPosition
	utf8Time       int64
	suffixRunes    []rune
	suffixHead     int
	suffixBytes    int
	suffixOverflow bool
}

func newHistoryDecoder(ctx context.Context, work *historyWorkDeadline, onLine func(historyDecodedLine)) *historyDecoder {
	return &historyDecoder{ctx: ctx, work: work, onLine: onLine}
}

func (d *historyDecoder) checkWork(_ int) error {
	if d.err != nil {
		return d.err
	}
	stopped, err := d.work.check(d.ctx)
	if err != nil {
		d.err = err
		return err
	}
	if stopped {
		d.err = errHistoryWorkDeadline
		return d.err
	}
	return nil
}

func (d *historyDecoder) feed(event historyEvent) error {
	if err := d.checkWork(0); err != nil {
		return err
	}
	for i, b := range event.data {
		if i != 0 && i&4095 == 0 {
			if err := d.checkWork(0); err != nil {
				return err
			}
		}
		position := event.position
		position.byte += i
		d.feedByte(b, position, event.timeMS)
		if d.err != nil {
			return d.err
		}
	}
	return nil
}

func (d *historyDecoder) setOrigin(position historyPosition, timeMS int64) {
	if d.lineOrigin == nil {
		copy := position
		d.lineOrigin = &copy
		d.lineTime = timeMS
	}
}

func (d *historyDecoder) startCSI() {
	d.ansi = 2
	d.csiLen = 0
}

func (d *historyDecoder) feedANSI(b byte) {
	switch d.ansi {
	case 1: // ESC followed by intermediates and one final byte.
		switch {
		case b == '[':
			d.startCSI()
		case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
			d.ansi = 3
		case b >= 0x20 && b <= 0x2f:
			d.ansi = 5
		case b == 0x1b:
			d.ansi = 1
		default:
			d.ansi = 0
		}
	case 2:
		switch {
		case b >= 0x40 && b <= 0x7e:
			d.applyCSI(b)
			d.ansi = 0
		case b >= 0x20 && b <= 0x3f:
			if d.csiLen < len(d.csi) {
				d.csi[d.csiLen] = b
				d.csiLen++
			}
		case b == 0x1b:
			d.ansi = 1
		default:
			d.ansi = 0
		}
	case 3: // OSC/DCS/SOS/PM/APC through BEL or ST.
		switch b {
		case 0x07:
			d.ansi = 0
		case 0x1b:
			d.ansi = 4
		case 0xc2:
			d.ansi = 6
		}
	case 4:
		if b == '\\' {
			d.ansi = 0
		} else if b != 0x1b {
			d.ansi = 3
		}
	case 5:
		if b < 0x20 || b > 0x2f {
			d.ansi = 0
		}
	case 6: // UTF-8 encoding of C1 ST while inside a control string.
		if b == 0x9c {
			d.ansi = 0
		} else {
			d.ansi = 3
		}
	}
}

func (d *historyDecoder) feedByte(b byte, position historyPosition, timeMS int64) {
	d.setOrigin(position, timeMS)
	if d.ansi != 0 {
		d.feedANSI(b)
		return
	}
	if len(d.utf8Buf) > 0 {
		if b < utf8.RuneSelf {
			d.flushUTF8(true)
		} else {
			d.utf8Buf = append(d.utf8Buf, b)
			d.flushUTF8(false)
			return
		}
	}
	if b >= utf8.RuneSelf {
		d.utf8Buf = append(d.utf8Buf, b)
		d.utf8Origin = position
		d.utf8Time = timeMS
		d.flushUTF8(false)
		return
	}
	d.feedRune(rune(b), position, timeMS)
}

func (d *historyDecoder) feedRune(r rune, position historyPosition, timeMS int64) {
	switch r {
	case 0x1b:
		d.ansi = 1
	case '\n', 0x84, 0x85:
		d.emitLine()
	case '\r':
		d.column = 0
		d.dirty = true
	case '\b':
		d.column = clampHistoryColumn(d.column)
		if d.column > 0 {
			d.column--
		}
	case '\t':
		d.column = clampHistoryColumn(d.column)
		next := historyColumnEnd(d.column, 8-(d.column&7), maxHistoryLineBytes)
		for d.column < next {
			d.writeRune(' ')
		}
	case 0x9b:
		d.startCSI()
	case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
		d.ansi = 3
	default:
		if r >= 0x20 && r != 0x7f && !(r >= 0x80 && r <= 0x9f) {
			d.writeRune(r)
		}
	}
}

func (d *historyDecoder) flushUTF8(final bool) {
	for len(d.utf8Buf) > 0 && (final || utf8.FullRune(d.utf8Buf)) {
		r, size := utf8.DecodeRune(d.utf8Buf)
		if r == utf8.RuneError && size == 1 && !final && len(d.utf8Buf) < utf8.UTFMax {
			return
		}
		d.utf8Buf = d.utf8Buf[size:]
		d.feedRune(r, d.utf8Origin, d.utf8Time)
	}
}

func clampHistoryColumn(column int) int {
	if column < 0 {
		return 0
	}
	if column > maxHistoryLineBytes {
		return maxHistoryLineBytes
	}
	return column
}

func historyColumnEnd(column, count, limit int) int {
	column = clampHistoryColumn(column)
	if column >= limit {
		return limit
	}
	if count <= 0 {
		return column
	}
	if count >= limit-column {
		return limit
	}
	return column + count
}

func (d *historyDecoder) csiParam(index, fallback int) int {
	const maxCSIParam = maxHistoryLineBytes + 1
	value, field := 0, 0
	have := false
	for i := 0; i <= d.csiLen; i++ {
		var b byte = ';'
		if i < d.csiLen {
			b = d.csi[i]
		}
		switch {
		case b >= '0' && b <= '9':
			digit := int(b - '0')
			if value > (maxCSIParam-digit)/10 {
				value = maxCSIParam
			} else {
				value = value*10 + digit
			}
			have = true
		case b == ';':
			if field == index {
				if !have || value == 0 {
					return fallback
				}
				return value
			}
			field++
			value, have = 0, false
		default:
			return fallback
		}
	}
	return fallback
}

func (d *historyDecoder) applyCSI(final byte) {
	d.column = clampHistoryColumn(d.column)
	n := d.csiParam(0, 1)
	switch final {
	case 'C', 'a':
		d.column = historyColumnEnd(d.column, n, maxHistoryLineBytes)
	case 'D':
		if n >= d.column {
			d.column = 0
		} else {
			d.column -= n
		}
	case 'G', '`':
		d.column = clampHistoryColumn(n - 1)
	case 'H', 'f':
		d.column = clampHistoryColumn(d.csiParam(1, 1) - 1)
	case 'K':
		d.eraseLine(d.csiParam(0, 0))
	case 'P':
		if d.column < len(d.cells) {
			end := historyColumnEnd(d.column, n, len(d.cells))
			copy(d.cells[d.column:], d.cells[end:])
			d.cells = d.cells[:len(d.cells)-(end-d.column)]
		}
	case 'X':
		if d.column < len(d.cells) {
			end := historyColumnEnd(d.column, n, len(d.cells))
			for i := d.column; i < end; i++ {
				d.cells[i] = ' '
			}
		}
	case '@':
		if d.column < maxHistoryLineBytes {
			for len(d.cells) < d.column {
				d.cells = append(d.cells, ' ')
			}
			n = historyColumnEnd(d.column, n, maxHistoryLineBytes) - d.column
			newLen := historyColumnEnd(len(d.cells), n, maxHistoryLineBytes)
			d.cells = append(d.cells, make([]rune, newLen-len(d.cells))...)
			copy(d.cells[d.column+n:], d.cells[d.column:newLen-n])
			for i, end := d.column, d.column+n; i < end; i++ {
				d.cells[i] = ' '
			}
		}
	}
}

func (d *historyDecoder) eraseLine(mode int) {
	d.dirty = true
	d.column = clampHistoryColumn(d.column)
	switch mode {
	case 0:
		if d.column < len(d.cells) {
			d.cells = d.cells[:d.column]
		}
	case 1:
		end := historyColumnEnd(d.column, 1, len(d.cells))
		for i := 0; i < end; i++ {
			d.cells[i] = ' '
		}
	case 2:
		for i := range d.cells {
			d.cells[i] = ' '
		}
	}
}

func (d *historyDecoder) recordSuffixRune(r rune) {
	size := utf8.RuneLen(r)
	if size < 0 {
		size = len(string(r))
	}
	d.suffixRunes = append(d.suffixRunes, r)
	d.suffixBytes += size
	for d.suffixBytes > maxHistoryLineBytes && d.suffixHead < len(d.suffixRunes) {
		d.suffixOverflow = true
		d.suffixBytes -= utf8.RuneLen(d.suffixRunes[d.suffixHead])
		d.suffixHead++
	}
	if d.suffixHead >= 4096 && d.suffixHead*2 >= len(d.suffixRunes) {
		copy(d.suffixRunes, d.suffixRunes[d.suffixHead:])
		d.suffixRunes = d.suffixRunes[:len(d.suffixRunes)-d.suffixHead]
		d.suffixHead = 0
	}
}

func (d *historyDecoder) writeRune(r rune) {
	d.dirty = true
	d.recordSuffixRune(r)
	d.column = clampHistoryColumn(d.column)
	if d.column >= maxHistoryLineBytes {
		return
	}
	for len(d.cells) < d.column {
		d.cells = append(d.cells, ' ')
	}
	if d.column < len(d.cells) {
		d.cells[d.column] = r
	} else {
		d.cells = append(d.cells, r)
	}
	d.column++
}

func (d *historyDecoder) emitLine() {
	d.flushUTF8(true)
	if d.lineOrigin == nil || d.err != nil {
		return
	}
	if err := d.checkWork(len(d.cells)); err != nil {
		return
	}
	text := strings.TrimRight(string(d.cells), " ")
	if len(text) > maxHistoryLineBytes {
		text = text[:maxHistoryLineBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	suffixText := ""
	if d.suffixOverflow {
		suffixText = strings.TrimRight(string(d.suffixRunes[d.suffixHead:]), " ")
	}
	line := historyDecodedLine{
		origin:     *d.lineOrigin,
		timeMS:     d.lineTime,
		text:       text,
		suffixText: suffixText,
	}
	d.onLine(line)
	d.cells = d.cells[:0]
	d.column = 0
	d.lineOrigin = nil
	d.lineTime = 0
	d.dirty = false
	d.suffixRunes = d.suffixRunes[:0]
	d.suffixHead = 0
	d.suffixBytes = 0
	d.suffixOverflow = false
}

func (d *historyDecoder) finish() error {
	if err := d.checkWork(0); err != nil {
		return err
	}
	d.ansi = 0
	d.flushUTF8(true)
	if d.dirty || len(d.cells) > 0 {
		d.emitLine()
	}
	return d.err
}
