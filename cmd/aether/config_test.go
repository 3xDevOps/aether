//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/secretscan"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/testhome"
)

// configControl serves config.roots, config.tree and config.import from the
// server's real config backend over a pipe, and records every config.import.
func configControl(t *testing.T, backend sshd.ConfigBackend, member domain.MemberID) (*protocol.Client, *[]protocol.ConfigImportParams) {
	t.Helper()
	imports := &[]protocol.ConfigImportParams{}
	server, client := net.Pipe()
	go func() {
		defer server.Close() //nolint:errcheck
		ctx := context.Background()
		r := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(r)
			if err != nil {
				return
			}
			var req protocol.Request
			if err = json.Unmarshal(line, &req); err != nil {
				return
			}
			var result any
			switch req.Method {
			case protocol.MethodConfigRoots:
				var roots []protocol.ConfigRoot
				roots, err = backend.Roots(ctx, member)
				result = protocol.ConfigRootsResult{Roots: roots}
			case protocol.MethodConfigTree:
				var p protocol.ConfigTreeParams
				_ = json.Unmarshal(req.Params, &p)
				var entries []protocol.ConfigTreeEntry
				entries, err = backend.Tree(ctx, member, p.Harness, p.Path)
				result = protocol.ConfigTreeResult{Entries: entries}
			case protocol.MethodConfigImport:
				var p protocol.ConfigImportParams
				_ = json.Unmarshal(req.Params, &p)
				*imports = append(*imports, p)
				files := make([]memberhome.ConfigFile, 0, len(p.Files))
				for _, f := range p.Files {
					content, _ := base64.StdEncoding.DecodeString(f.ContentBase64)
					files = append(files, memberhome.ConfigFile{Path: f.Path, Content: content, Mode: f.Mode})
				}
				result, err = backend.Import(ctx, member, p.Harness, files)
			default:
				err = fmt.Errorf("unexpected method %s", req.Method)
			}
			resp := protocol.Response{JSONRPC: "2.0", ID: req.ID}
			if err != nil {
				resp.Error = &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error()}
			} else {
				resp.Result, _ = json.Marshal(result)
			}
			out, _ := json.Marshal(resp)
			if _, err = server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	return c, imports
}

func TestConfigPushUploadsChosenPathsAndLeavesOutWhatTheServerWould(t *testing.T) {
	secret, err := os.ReadFile(filepath.Join("..", "..", "internal", "cli", "profile", "testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	home := testhome.Isolate(t)
	claude := filepath.Join(home, ".claude")
	for rel, file := range map[string]struct {
		content string
		mode    os.FileMode
	}{
		"settings.json":          {`{"model":"opus"}`, 0o644},
		"CLAUDE.md":              {"# Instructions\n", 0o644},
		"commands/review.md":     {"Review the diff.\n", 0o644},
		"skills/deploy/SKILL.md": {"# Deploy\n", 0o644},
		"skills/deploy/run.sh":   {"#!/bin/sh\necho deploy\n", 0o755},
		"skills/deploy/key.pem":  {"not read\n", 0o600},
		".credentials.json":      {"not read\n", 0o600},
		".aether-profile-ignore": {"*.log\n", 0o644},
		"history.jsonl":          {"{}\n", 0o644},
		"projects/abc/s.jsonl":   {"{}\n", 0o644},
		"notes.md":               {string(secret), 0o644},
	} {
		path := filepath.Join(claude, filepath.FromSlash(rel))
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(file.content), file.mode); err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(path, file.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Symlink(filepath.Join(home, "outside.md"), filepath.Join(claude, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err = syscall.Mkfifo(filepath.Join(claude, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(claude, "big.bin"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(filepath.Join(claude, "big.bin"), 65<<20); err != nil {
		t.Fatal(err)
	}
	hits := secretscan.Scan("notes.md", secret)
	if len(hits) == 0 {
		t.Fatal("test secret is not flagged by the scanner")
	}
	secretLine := fmt.Sprintf("notes.md: secret detected at %s (%s)\n", hits[0].Location, hits[0].Kind)

	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	const member = domain.MemberID("member-1")
	serverHome, err := homes.Path(member)
	if err != nil {
		t.Fatal(err)
	}
	c, imports := configControl(t, sshd.NewConfigBackend(homes, nil), member)

	var out bytes.Buffer
	if err = configList(c, &out); err != nil {
		t.Fatalf("config ls: %v", err)
	}
	for _, want := range []string{
		"claude (Claude Code): ~/.claude, exists on this computer\n",
		"codex (Codex): ~/.codex, does not exist on this computer\n",
		"  always left out, runtime and history: projects/, shell-snapshots/, statsig/, todos/, file-history/, history.jsonl, daemon/\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("config ls output lacks %q:\n%s", want, out.String())
		}
	}

	localLeftOut := []string{
		".aether-profile-ignore: Aether profile ignore file\n",
		".credentials.json: credential\n",
		"big.bin: 68157440 bytes is over the 64 MiB upload limit; copy it onto the server from aether terminal\n",
		"fifo: not a regular file\n",
		"history.jsonl: runtime or history path\n",
		"link.md: symlink, not followed\n",
		"projects/: runtime or history path\n",
		"skills/deploy/key.pem: credential\n",
	}
	uploadBytes := len(`{"model":"opus"}`) + len("# Instructions\n") + len("Review the diff.\n") + len("# Deploy\n") + len("#!/bin/sh\necho deploy\n")

	out.Reset()
	if err = configPush(c, &out, "claude", []string{"."}, true); err != nil {
		t.Fatalf("config push --dry-run .: %v", err)
	}
	want := "would leave out " + strings.Join(localLeftOut, "would leave out ") +
		"would upload CLAUDE.md\n" +
		"would upload commands/review.md\n" +
		"would leave out " + secretLine +
		"would upload settings.json\n" +
		"would upload skills/deploy/SKILL.md\n" +
		"would upload skills/deploy/run.sh\n" +
		fmt.Sprintf("would upload 5 files (%d bytes) to ~/.claude; would leave out 9\n", uploadBytes)
	if out.String() != want {
		t.Errorf("dry-run output:\n%s\nwant:\n%s", out.String(), want)
	}
	if len(*imports) != 0 {
		t.Fatalf("--dry-run sent %d config.import requests", len(*imports))
	}
	if _, err = os.Stat(filepath.Join(serverHome, ".claude")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("--dry-run created the server directory: %v", err)
	}

	out.Reset()
	if err = configPush(c, &out, "claude", []string{"skills"}, false); err != nil {
		t.Fatalf("config push skills: %v", err)
	}
	want = "left out skills/deploy/key.pem: credential\n" +
		"uploaded skills/deploy/SKILL.md\n" +
		"uploaded skills/deploy/run.sh\n" +
		fmt.Sprintf("uploaded 2 files (%d bytes) to ~/.claude; left out 1\n", len("# Deploy\n")+len("#!/bin/sh\necho deploy\n"))
	if out.String() != want {
		t.Errorf("push skills output:\n%s\nwant:\n%s", out.String(), want)
	}
	for rel, mode := range map[string]os.FileMode{"skills/deploy/run.sh": 0o755, "skills/deploy/SKILL.md": 0o644} {
		info, statErr := os.Stat(filepath.Join(serverHome, ".claude", filepath.FromSlash(rel)))
		if statErr != nil {
			t.Fatalf("server copy of %s: %v", rel, statErr)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("server mode of %s = %v, want %v", rel, info.Mode().Perm(), mode)
		}
	}

	out.Reset()
	if err = configPush(c, &out, "claude", []string{filepath.Join(claude, "commands"), "commands/review.md"}, false); err != nil {
		t.Fatalf("config push of an absolute path: %v", err)
	}
	if want = fmt.Sprintf("uploaded commands/review.md\nuploaded 1 file (%d bytes) to ~/.claude; left out 0\n", len("Review the diff.\n")); out.String() != want {
		t.Errorf("push commands output:\n%s\nwant:\n%s", out.String(), want)
	}
	if last := (*imports)[len(*imports)-1]; len(last.Files) != 1 || last.Files[0].Path != "commands/review.md" {
		t.Errorf("overlapping paths sent %+v, want commands/review.md once", last.Files)
	}

	out.Reset()
	if err = configPush(c, &out, "claude", []string{"."}, false); err != nil {
		t.Fatalf("config push .: %v", err)
	}
	want = "left out " + strings.Join(localLeftOut, "left out ") +
		"left out " + secretLine +
		"uploaded CLAUDE.md\n" +
		"uploaded commands/review.md\n" +
		"uploaded settings.json\n" +
		"uploaded skills/deploy/SKILL.md\n" +
		"uploaded skills/deploy/run.sh\n" +
		fmt.Sprintf("uploaded 5 files (%d bytes) to ~/.claude; left out 9\n", uploadBytes)
	if out.String() != want {
		t.Errorf("push . output:\n%s\nwant:\n%s", out.String(), want)
	}
	if len(*imports) != 3 {
		t.Errorf("three pushes sent %d config.import requests", len(*imports))
	}
	for _, f := range (*imports)[len(*imports)-1].Files {
		if f.Path == "skills/deploy/run.sh" && f.Mode != 0o755 {
			t.Errorf("run.sh sent with mode %o, want 755", f.Mode)
		}
	}

	out.Reset()
	if err = configTree(c, &out, "claude", ""); err != nil {
		t.Fatalf("config ls claude: %v", err)
	}
	want = fmt.Sprintf("file\t%d\tCLAUDE.md\ndir\tcommands\nfile\t%d\tsettings.json\ndir\tskills\n", len("# Instructions\n"), len(`{"model":"opus"}`))
	if out.String() != want {
		t.Errorf("config ls claude:\n%s\nwant:\n%s", out.String(), want)
	}

	for _, path := range []string{"../outside.md", filepath.Join(home, "outside.md")} {
		err = configPush(c, &out, "claude", []string{"settings.json", path}, false)
		if err == nil || !strings.Contains(err.Error(), path+" is outside ~/.claude") {
			t.Errorf("push of %s: %v, want an outside-the-directory error", path, err)
		}
	}
	if err = configPush(c, &out, "claude", []string{"missing.md"}, false); !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "missing.md") {
		t.Errorf("push of a missing path: %v", err)
	}
	if err = configPush(c, &out, "nope", []string{"."}, false); err == nil || !strings.Contains(err.Error(), `agent "nope"`) || !strings.Contains(err.Error(), "aether config ls") {
		t.Errorf("push for an unknown agent: %v", err)
	}
	if len(*imports) != 3 {
		t.Errorf("failed pushes sent config.import requests: %d total", len(*imports))
	}
}
