package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"sync"
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

func TestRenderChildrenStripsControlCharacters(t *testing.T) {
	var b bytes.Buffer
	renderChildren(&b, "run-1", protocol.RunChildrenResult{Children: []protocol.Child{{RunID: "c1", Harness: "codex", Purpose: "deploy\x1b]8;;http://evil\x07helper"}}})
	if strings.ContainsAny(b.String(), "\x1b\x07") {
		t.Fatalf("control bytes reached the terminal output: %q", b.String())
	}
}

// configTreeAt neutralizes every printed name, including ancestors of nested
// entries, while still asking the server for the raw names.
func TestConfigTreeAtPrintsSafeNamesButRequestsRawOnes(t *testing.T) {
	const dir = "d\x1b]0;x\x07"
	server, client := net.Pipe()
	var mu sync.Mutex
	var asked []string
	go func() {
		defer server.Close() //nolint:errcheck
		r := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(r)
			if err != nil {
				return
			}
			var req protocol.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}
			var p protocol.ConfigTreeParams
			_ = json.Unmarshal(req.Params, &p)
			mu.Lock()
			asked = append(asked, p.Path)
			mu.Unlock()
			res := protocol.ConfigTreeResult{Entries: []protocol.ConfigTreeEntry{{Name: dir, Kind: "dir"}}}
			if strings.Contains(p.Path, dir) {
				res = protocol.ConfigTreeResult{Entries: []protocol.ConfigTreeEntry{{Name: "f\x1b[2K", Kind: "file", Size: 1}}}
			}
			raw, _ := json.Marshal(res)
			out, _ := json.Marshal(protocol.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
			if _, err := server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	var b bytes.Buffer
	if err := configTreeAt(c, &b, protocol.ConfigRoot{Harness: "claude", Path: "~/.claude"}, ".", "", true); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(b.String(), "\x1b\x07") {
		t.Fatalf("control bytes reached the terminal output: %q", b.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 || !strings.Contains(asked[1], dir) {
		t.Fatalf("server was asked for %q, want the raw directory name in the nested request", asked)
	}
}
