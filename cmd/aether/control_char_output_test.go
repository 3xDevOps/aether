package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// Agent-authored run Title/Task must not carry terminal control bytes into the
// operator's terminal when listed.
func TestRenderRunsStripsControlCharacters(t *testing.T) {
	var b bytes.Buffer
	runs := []protocol.Run{{
		ID:     "run-aaaaaaaaaa",
		Status: "running",
		Title:  "t\x1b]0;spoof\x07",
		Task:   "x\r\x1b[2K",
	}}
	if err := renderRuns(&b, runs, func(string) string { return "m" }, map[string]string{}, false, false); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(b.String(), "\x1b\x07\r") {
		t.Fatalf("control bytes reached the terminal output: %q", b.String())
	}
}

func TestPrintableStripsC0AndDel(t *testing.T) {
	if got := printable("a\x1bb\x07\x7fc"); got != "a b  c" {
		t.Fatalf("printable = %q", got)
	}
}
