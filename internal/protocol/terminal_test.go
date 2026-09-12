package protocol

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"
)

func TestTerminalRecordsPreserveOutputGeometryOrder(t *testing.T) {
	var wire bytes.Buffer
	before, after := "before\x00\xff", "\x1b[18;60HX"
	if _, err := WriteTerminalOutput(&wire, []byte(before)); err != nil {
		t.Fatal(err)
	}
	if err := WriteTerminalGeometry(&wire, 60, 18); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteTerminalOutput(&wire, []byte(after)); err != nil {
		t.Fatal(err)
	}
	r := TerminalReader{Reader: &wire}
	var output bytes.Buffer
	var events []string
	buf := make([]byte, 3)
	for {
		n, size, err := r.Read(buf)
		output.Write(buf[:n])
		if size != [2]uint{} {
			if n != 0 || size != [2]uint{60, 18} {
				t.Fatalf("geometry event: size=%v output=%q", size, buf[:n])
			}
			events = append(events, output.String())
			output.Reset()
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	events = append(events, output.String())
	if !slices.Equal(events, []string{before, after}) {
		t.Fatalf("output around geometry = %q", events)
	}
}

func TestTerminalRecordsRejectTruncatedFrames(t *testing.T) {
	for name, data := range map[string][]byte{
		"output length": {'o'},
		"geometry":      {'g', 0, 0, 0, 80},
		"output body":   {'o', 0, 0, 0, 3, 'x'},
	} {
		t.Run(name, func(t *testing.T) {
			r := TerminalReader{Reader: bytes.NewReader(data)}
			_, _, err := r.Read(make([]byte, 32))
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("truncated record error = %v, want unexpected EOF", err)
			}
		})
	}
}
