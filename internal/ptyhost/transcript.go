package ptyhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const transcriptFlushInterval = 2 * time.Second

type castHeader struct {
	Version   int               `json:"version"`
	Width     uint              `json:"width"`
	Height    uint              `json:"height"`
	Timestamp int64             `json:"timestamp"`
	Env       map[string]string `json:"env"`
}

// castWriter appends asciinema cast v2 events (output, resize, marker) to
// the run's transcript file. Output only - PTY input is never recorded.
// Output chunks that end mid-rune hold the incomplete UTF-8 tail back until
// the next chunk so runes are recorded whole; bytes that are not valid
// UTF-8 are escaped losslessly (see appendCastString), so replay through
// decodeCastString reproduces the live byte stream exactly.
type castWriter struct {
	mu      sync.Mutex
	f       *os.File
	bw      *bufio.Writer
	start   time.Time
	pending []byte
	closed  bool
	stop    chan struct{}
}

func newCastWriter(path string, cols, rows uint) (*castWriter, error) {
	if err := renameAsideTranscript(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ptyhost: create transcript: %w", err)
	}
	w := &castWriter{
		f:     f,
		bw:    bufio.NewWriterSize(f, 32*1024),
		start: time.Now(),
		stop:  make(chan struct{}),
	}
	hdr, err := json.Marshal(castHeader{
		Version:   2,
		Width:     cols,
		Height:    rows,
		Timestamp: w.start.Unix(),
		Env:       map[string]string{"TERM": "xterm-256color"},
	})
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("ptyhost: transcript header: %w", err)
	}
	if _, err := w.bw.Write(append(hdr, '\n')); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("ptyhost: transcript header: %w", err)
	}
	go w.flushLoop()
	return w, nil
}

// renameAsideTranscript preserves an existing non-empty transcript (a prior
// incarnation of the run, e.g. before a reboot-recovery restart) by renaming
// it to <run-id>.<unix-nanos>.cast. Transcripts are never truncated.
func renameAsideTranscript(path string) error {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return nil
	}
	aside := fmt.Sprintf("%s.%d.cast", strings.TrimSuffix(path, ".cast"), time.Now().UnixNano())
	if err := os.Rename(path, aside); err != nil {
		return fmt.Errorf("ptyhost: preserve prior transcript: %w", err)
	}
	return nil
}

// readCastTail decodes output events from the bounded tail of an asciinema
// v2 transcript. A partial first line is discarded because the read window
// may begin in the middle of a JSON event.
func readCastTail(path string, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	window := int64(maxBytes) * 4
	if window < 0 || window > info.Size() {
		window = info.Size()
	}
	if window == 0 {
		return nil, nil
	}
	raw := make([]byte, int(window))
	if _, err := io.ReadFull(io.NewSectionReader(f, info.Size()-window, window), raw); err != nil {
		return nil, err
	}
	if info.Size() > window {
		i := bytes.IndexByte(raw, '\n')
		if i < 0 {
			return nil, nil
		}
		raw = raw[i+1:]
	}

	out := make([]byte, 0, maxBytes)
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event []json.RawMessage
		if err := json.Unmarshal(line, &event); err != nil || len(event) < 3 {
			continue
		}
		var code string
		if err := json.Unmarshal(event[1], &code); err != nil || code != "o" {
			continue
		}
		data, err := decodeCastString(event[2])
		if err != nil {
			continue
		}
		out = append(out, data...)
	}
	if len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
		// The cut can land mid-escape-sequence or mid-rune; replay must
		// start on a line boundary, the same rule the replay ring applies.
		if i := bytes.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return out, nil
}

func (w *castWriter) flushLoop() {
	t := time.NewTicker(transcriptFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.mu.Lock()
			if !w.closed {
				_ = w.bw.Flush()
			}
			w.mu.Unlock()
		}
	}
}

func (w *castWriter) output(p []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	data := append(w.pending, p...)
	cut := utf8Boundary(data)
	w.pending = append([]byte(nil), data[cut:]...)
	if cut > 0 {
		w.eventLocked("o", data[:cut])
	}
}

func (w *castWriter) resize(cols, rows uint) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.eventLocked("r", fmt.Appendf(nil, "%dx%d", cols, rows))
}

func (w *castWriter) marker(text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.eventLocked("m", []byte(text))
}

func (w *castWriter) eventLocked(code string, data []byte) {
	line := make([]byte, 0, len(data)+32)
	line = append(line, '[')
	line = strconv.AppendFloat(line, time.Since(w.start).Seconds(), 'f', 6, 64)
	line = append(line, ',', '"')
	line = append(line, code...)
	line = append(line, '"', ',')
	line = appendCastString(line, data)
	line = append(line, ']', '\n')
	_, _ = w.bw.Write(line)
}

func (w *castWriter) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	close(w.stop)
	if len(w.pending) > 0 {
		w.eventLocked("o", w.pending)
		w.pending = nil
	}
	_ = w.bw.Flush()
	_ = w.f.Sync()
	return w.f.Close()
}

// utf8Boundary returns the length of the longest prefix of p that does not
// end in the middle of a (possibly still incoming) UTF-8 sequence. Invalid
// sequences pass through whole.
func utf8Boundary(p []byte) int {
	n := len(p)
	for i := 1; i <= 3 && i <= n; i++ {
		b := p[n-i]
		if b < 0x80 {
			return n
		}
		if b >= 0xC0 { // leading byte: is the sequence complete?
			var need int
			switch {
			case b >= 0xF0:
				need = 4
			case b >= 0xE0:
				need = 3
			default:
				need = 2
			}
			if i < need {
				return n - i
			}
			return n
		}
	}
	return n
}

const hexDigits = "0123456789abcdef"

// appendCastString appends p to dst as a JSON string. Valid UTF-8 passes
// through unchanged; every byte that is not part of a valid UTF-8 sequence
// is written as the lone low surrogate escape \udcXX (0xDC00+byte,
// surrogate-escape convention), which keeps arbitrary binary PTY output
// lossless: decodeCastString maps the escapes back to the original bytes.
// Standard cast players render such escapes as replacement characters.
func appendCastString(dst, p []byte) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(p); {
		b := p[i]
		if b < utf8.RuneSelf {
			switch {
			case b == '"' || b == '\\':
				dst = append(dst, '\\', b)
			case b >= 0x20:
				dst = append(dst, b)
			case b == '\n':
				dst = append(dst, '\\', 'n')
			case b == '\r':
				dst = append(dst, '\\', 'r')
			case b == '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = appendUnicodeEscape(dst, rune(b))
			}
			i++
			continue
		}
		r, size := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && size == 1 {
			dst = appendUnicodeEscape(dst, 0xDC00+rune(b))
			i++
			continue
		}
		dst = append(dst, p[i:i+size]...)
		i += size
	}
	return append(dst, '"')
}

func appendUnicodeEscape(dst []byte, r rune) []byte {
	return append(dst, '\\', 'u',
		hexDigits[r>>12&0xF], hexDigits[r>>8&0xF], hexDigits[r>>4&0xF], hexDigits[r&0xF])
}

var errBadCastString = errors.New("ptyhost: malformed cast string")

// decodeCastString decodes a raw JSON string token as written by
// appendCastString back into the exact recorded bytes: lone low surrogate
// escapes \udc80–\udcff become the raw bytes they stand for, surrogate
// pairs and all standard escapes decode as usual.
func decodeCastString(tok []byte) ([]byte, error) {
	if len(tok) < 2 || tok[0] != '"' || tok[len(tok)-1] != '"' {
		return nil, errBadCastString
	}
	tok = tok[1 : len(tok)-1]
	out := make([]byte, 0, len(tok))
	for i := 0; i < len(tok); {
		b := tok[i]
		if b != '\\' {
			out = append(out, b)
			i++
			continue
		}
		if i+1 >= len(tok) {
			return nil, errBadCastString
		}
		switch e := tok[i+1]; e {
		case '"', '\\', '/':
			out = append(out, e)
			i += 2
		case 'b':
			out = append(out, '\b')
			i += 2
		case 'f':
			out = append(out, '\f')
			i += 2
		case 'n':
			out = append(out, '\n')
			i += 2
		case 'r':
			out = append(out, '\r')
			i += 2
		case 't':
			out = append(out, '\t')
			i += 2
		case 'u':
			r, n, err := decodeUnicodeEscape(tok[i:])
			if err != nil {
				return nil, err
			}
			if r >= 0xDC80 && r <= 0xDCFF {
				out = append(out, byte(r&0xFF)) // surrogate-escaped raw byte
			} else {
				out = utf8.AppendRune(out, r)
			}
			i += n
		default:
			return nil, errBadCastString
		}
	}
	return out, nil
}

// decodeUnicodeEscape decodes a \uXXXX escape (combining surrogate pairs)
// at the start of tok, returning the rune and the bytes consumed.
func decodeUnicodeEscape(tok []byte) (rune, int, error) {
	r, err := hex4(tok, 2)
	if err != nil {
		return 0, 0, err
	}
	if r >= 0xD800 && r < 0xDC00 { // high surrogate: needs a low surrogate
		if len(tok) < 12 || tok[6] != '\\' || tok[7] != 'u' {
			return 0, 0, errBadCastString
		}
		lo, err := hex4(tok, 8)
		if err != nil || lo < 0xDC00 || lo >= 0xE000 {
			return 0, 0, errBadCastString
		}
		return 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00), 12, nil
	}
	return r, 6, nil
}

func hex4(tok []byte, at int) (rune, error) {
	if len(tok) < at+4 {
		return 0, errBadCastString
	}
	var r rune
	for _, c := range tok[at : at+4] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, errBadCastString
		}
	}
	return r, nil
}

// replayReader decodes a transcript cast file back into the raw terminal
// bytes: each [time,"o",<string>] line yields the exact bytes
// appendCastString recorded; the header, resize, and marker events are
// skipped. Decoding is line-incremental so a large transcript streams
// instead of loading whole.
type replayReader struct {
	f   *os.File
	br  *bufio.Reader
	buf []byte
	err error
}

func (r *replayReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 && r.err == nil {
		r.err = r.next()
	}
	if len(r.buf) == 0 {
		return 0, r.err
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// next decodes the next event line into r.buf (empty for non-output
// events), returning io.EOF once the transcript is exhausted.
func (r *replayReader) next() error {
	line, err := r.br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("ptyhost: read transcript: %w", err)
	}
	atEnd := errors.Is(err, io.EOF)
	done := func() error {
		if atEnd {
			return io.EOF
		}
		return nil
	}
	line = bytes.TrimSpace(line)
	// Blank tail or the cast v2 header object: nothing to emit.
	if len(line) == 0 || line[0] == '{' {
		return done()
	}
	var event []json.RawMessage
	if uerr := json.Unmarshal(line, &event); uerr != nil || len(event) < 3 {
		return fmt.Errorf("ptyhost: malformed transcript event: %w", errBadCastString)
	}
	if string(event[1]) != `"o"` {
		return done()
	}
	data, derr := decodeCastString(event[2])
	if derr != nil {
		return fmt.Errorf("ptyhost: decode transcript output: %w", derr)
	}
	r.buf = data
	return done()
}

func (r *replayReader) Close() error { return r.f.Close() }
