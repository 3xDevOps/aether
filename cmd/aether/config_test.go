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
	"slices"
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

const configMember = domain.MemberID("member-1")

// configControl serves the config methods from backend over a pipe, reports
// claude as the one installed agent, and records every config.import.
func configControl(t *testing.T, backend sshd.ConfigBackend) (*protocol.Client, *[]protocol.ConfigImportParams) {
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
				roots, err = backend.Roots(ctx, configMember)
				result = protocol.ConfigRootsResult{Roots: roots}
			case protocol.MethodConfigTree:
				var p protocol.ConfigTreeParams
				_ = json.Unmarshal(req.Params, &p)
				var entries []protocol.ConfigTreeEntry
				entries, err = backend.Tree(ctx, configMember, p.Harness, p.Path)
				result = protocol.ConfigTreeResult{Entries: entries}
			case protocol.MethodConfigRead:
				var p protocol.ConfigReadParams
				_ = json.Unmarshal(req.Params, &p)
				result, err = backend.Read(ctx, configMember, p.Harness, p.Path)
			case protocol.MethodConfigStat:
				var p protocol.ConfigStatParams
				_ = json.Unmarshal(req.Params, &p)
				var entries []protocol.ConfigStatEntry
				entries, err = backend.Stat(ctx, configMember, p.Harness, p.Paths)
				result = protocol.ConfigStatResult{Entries: entries}
			case protocol.MethodAgentList:
				result = protocol.AgentListResult{Agents: []protocol.AgentInfo{{Name: "claude", Installed: true}, {Name: "codex"}}}
			case protocol.MethodConfigImport:
				var p protocol.ConfigImportParams
				_ = json.Unmarshal(req.Params, &p)
				*imports = append(*imports, p)
				files := make([]memberhome.ConfigFile, 0, len(p.Files))
				for _, f := range p.Files {
					content, _ := base64.StdEncoding.DecodeString(f.ContentBase64)
					files = append(files, memberhome.ConfigFile{Path: f.Path, Content: content, Mode: f.Mode})
				}
				result, err = backend.Import(ctx, configMember, p.Harness, files, p.Overwrite)
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

// configServer returns the server's real config backend over fresh member
// homes, and the member's home on the server.
func configServer(t *testing.T) (*sshd.HomeConfigBackend, string) {
	t.Helper()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	serverHome, err := homes.Path(configMember)
	if err != nil {
		t.Fatal(err)
	}
	return sshd.NewConfigBackend(homes, nil), serverHome
}

// configImportReply replaces the backend's config.import reply.
type configImportReply struct {
	sshd.ConfigBackend
	reply func(files []memberhome.ConfigFile) (protocol.ConfigImportResult, error)
}

func (b configImportReply) Import(_ context.Context, _ domain.MemberID, _ string, files []memberhome.ConfigFile, _ bool) (protocol.ConfigImportResult, error) {
	return b.reply(files)
}

// configImported replies to config.import as the server does when it writes
// every file, without the server's per-file fsync.
func configImported(files []memberhome.ConfigFile) (protocol.ConfigImportResult, error) {
	result := protocol.ConfigImportResult{Harness: "claude", Files: len(files), Excluded: []protocol.ConfigExcluded{}}
	for _, f := range files {
		result.Bytes += int64(len(f.Content))
	}
	return result, nil
}

func writeConfigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
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
		".env":                   {"not read\n", 0o600},
		"plugins/cache/p/x.js":   {"not read\n", 0o644},
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
	writeConfigFile(t, filepath.Join(home, "private", "notes.md"), "private\n")
	if err = os.Symlink(filepath.Join(home, "private"), filepath.Join(claude, "skills", "linked")); err != nil {
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

	backend, serverHome := configServer(t)
	c, imports := configControl(t, backend)

	var out bytes.Buffer
	if err = configList(c, &out); err != nil {
		t.Fatalf("config ls: %v", err)
	}
	for _, want := range []string{
		"claude (Claude Code): ~/.claude, exists on this computer, installed on the server\n",
		"codex (Codex): ~/.codex, does not exist on this computer, not installed on the server\n",
		"agent.db-shm, *.pem, .env, .env.*\n",
		"  always left out, runtime and history: projects/, shell-snapshots/, statsig/, todos/, file-history/, history.jsonl, daemon/, transcripts/, sessions/, session-env/, cache/, backups/, plugins/cache/, plugins/marketplaces/, plugins/synced/, skills/synced/\n",
		"*.sqlite, *.sqlite-shm, *.sqlite-wal\n",
		"  always left out, runtime and history: node_modules/, .git/\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("config ls output lacks %q:\n%s", want, out.String())
		}
	}

	localLeftOut := []string{
		".aether-profile-ignore: Aether profile ignore file\n",
		".credentials.json: credential\n",
		".env: credential\n",
		"big.bin: 68157440 bytes is over the 64 MiB upload limit; copy it onto the server from aether terminal\n",
		"fifo: not a regular file\n",
		"history.jsonl: runtime or history path\n",
		"link.md: symlink, not followed\n",
		"plugins/cache/: runtime or history path\n",
		"projects/: runtime or history path\n",
		"skills/deploy/key.pem: credential\n",
		"skills/linked: symlink, not followed\n",
	}
	uploadBytes := len(`{"model":"opus"}`) + len("# Instructions\n") + len("Review the diff.\n") + len("# Deploy\n") + len("#!/bin/sh\necho deploy\n")
	push := func(want string, opts configPushOptions, paths ...string) error {
		t.Helper()
		out.Reset()
		pushErr := configPush(c, &out, "claude", paths, opts)
		if out.String() != want {
			t.Errorf("config push %v %+v printed:\n%s\nwant:\n%s", paths, opts, out.String(), want)
		}
		return pushErr
	}
	serverFile := func(rel string) string {
		t.Helper()
		content, readErr := os.ReadFile(filepath.Join(serverHome, ".claude", filepath.FromSlash(rel)))
		if readErr != nil {
			t.Fatalf("server copy of %s: %v", rel, readErr)
		}
		return string(content)
	}

	if err = push("would leave out "+strings.Join(localLeftOut, "would leave out ")+
		"would create CLAUDE.md\n"+
		"would create commands/review.md\n"+
		"would leave out "+secretLine+
		"would create settings.json\n"+
		"would create skills/deploy/SKILL.md\n"+
		"would create skills/deploy/run.sh\n"+
		fmt.Sprintf("would create 5, replace 0, skip 0 identical, refuse 0, leave out 12 in ~/.claude (%d bytes to upload)\n", uploadBytes),
		configPushOptions{dryRun: true}, "."); err != nil {
		t.Fatalf("config push --dry-run .: %v", err)
	}
	if len(*imports) != 0 {
		t.Fatalf("--dry-run sent %d config.import requests", len(*imports))
	}
	if _, err = os.Stat(filepath.Join(serverHome, ".claude")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("--dry-run created the server directory: %v", err)
	}

	if err = push("left out skills/deploy/key.pem: credential\n"+
		"left out skills/linked: symlink, not followed\n"+
		"created skills/deploy/SKILL.md\n"+
		"created skills/deploy/run.sh\n"+
		fmt.Sprintf("created 2, replaced 0, skipped 0 identical, refused 0, left out 2 in ~/.claude (%d bytes uploaded)\n", len("# Deploy\n")+len("#!/bin/sh\necho deploy\n")),
		configPushOptions{}, "skills"); err != nil {
		t.Fatalf("config push skills: %v", err)
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

	if err = push(fmt.Sprintf("created commands/review.md\ncreated 1, replaced 0, skipped 0 identical, refused 0, left out 0 in ~/.claude (%d bytes uploaded)\n", len("Review the diff.\n")),
		configPushOptions{}, filepath.Join(claude, "commands"), "commands/review.md"); err != nil {
		t.Fatalf("config push of an absolute path: %v", err)
	}
	if last := (*imports)[len(*imports)-1]; len(last.Files) != 1 || last.Files[0].Path != ".claude/commands/review.md" {
		t.Errorf("overlapping paths sent %+v, want .claude/commands/review.md once", last.Files)
	}

	if err = push("left out "+strings.Join(localLeftOut, "left out ")+
		"created CLAUDE.md\n"+
		"skipped commands/review.md: identical on the server\n"+
		"left out "+secretLine+
		"created settings.json\n"+
		"skipped skills/deploy/SKILL.md: identical on the server\n"+
		"skipped skills/deploy/run.sh: identical on the server\n"+
		fmt.Sprintf("created 2, replaced 0, skipped 3 identical, refused 0, left out 12 in ~/.claude (%d bytes uploaded)\n", len("# Instructions\n")+len(`{"model":"opus"}`)),
		configPushOptions{}, "."); err != nil {
		t.Fatalf("config push .: %v", err)
	}
	if len(*imports) != 3 || len((*imports)[2].Files) != 2 {
		t.Errorf("three pushes sent %d config.import requests, the last with %d files; want 3 and only the 2 new files", len(*imports), len((*imports)[len(*imports)-1].Files))
	}
	for _, f := range (*imports)[0].Files {
		if f.Path == ".claude/skills/deploy/run.sh" && f.Mode != 0o755 {
			t.Errorf("run.sh sent with mode %o, want 755", f.Mode)
		}
	}

	out.Reset()
	if err = configTree(c, &out, "claude", "", false); err != nil {
		t.Fatalf("config ls claude: %v", err)
	}
	want := fmt.Sprintf("file\t%d\tCLAUDE.md\ndir\tcommands\nfile\t%d\tsettings.json\ndir\tskills\n", len("# Instructions\n"), len(`{"model":"opus"}`))
	if out.String() != want {
		t.Errorf("config ls claude:\n%s\nwant:\n%s", out.String(), want)
	}
	out.Reset()
	if err = configTree(c, &out, "claude", "skills/", true); err != nil {
		t.Fatalf("config ls -r claude skills/: %v", err)
	}
	if want = "dir\tdeploy\nfile\t9\tdeploy/SKILL.md\nfile\t22\tdeploy/run.sh\n"; out.String() != want {
		t.Errorf("config ls -r claude skills/:\n%s\nwant:\n%s", out.String(), want)
	}

	out.Reset()
	if err = configCat(c, &out, "claude", "settings.json"); err != nil || out.String() != `{"model":"opus"}` {
		t.Errorf("config cat claude settings.json = %q, %v", out.String(), err)
	}
	if err = configCat(c, &out, "claude", ".credentials.json"); err == nil {
		t.Error("config cat printed a credential file")
	}

	writeConfigFile(t, filepath.Join(claude, "settings.json"), `{"model":"sonnet"}`)
	different := "settings.json: the server has a different file (16 bytes, 18 here)"
	if err = push("would refuse "+different+"; --overwrite replaces it\n"+
		"would skip CLAUDE.md: identical on the server\n"+
		"would create 0, replace 0, skip 1 identical, refuse 1, leave out 0 in ~/.claude (0 bytes to upload)\n",
		configPushOptions{dryRun: true}, "settings.json", "CLAUDE.md"); err != nil {
		t.Fatalf("config push --dry-run of a differing file: %v", err)
	}
	err = push("refused "+different+"; --overwrite replaces it\n"+
		"created 0, replaced 0, skipped 0 identical, refused 1, left out 0 in ~/.claude (0 bytes uploaded)\n",
		configPushOptions{}, "settings.json")
	if err == nil || !strings.Contains(err.Error(), "refused 1") || !strings.Contains(err.Error(), "aether config cat claude <path>") {
		t.Errorf("push of a differing file: %v, want a refusal", err)
	}
	if got := serverFile("settings.json"); got != `{"model":"opus"}` || len(*imports) != 3 {
		t.Errorf("a refused push left the server with %q after %d requests, want the old file and no new request", got, len(*imports))
	}
	if err = push("would replace "+different+"\n"+
		"would create 0, replace 1, skip 0 identical, refuse 0, leave out 0 in ~/.claude (18 bytes to upload)\n",
		configPushOptions{dryRun: true, overwrite: true}, "settings.json"); err != nil {
		t.Fatalf("config push --dry-run --overwrite: %v", err)
	}
	if err = push("replaced settings.json\n"+
		"created 0, replaced 1, skipped 0 identical, refused 0, left out 0 in ~/.claude (18 bytes uploaded)\n",
		configPushOptions{overwrite: true}, "settings.json"); err != nil {
		t.Fatalf("config push --overwrite: %v", err)
	}
	if got := serverFile("settings.json"); got != `{"model":"sonnet"}` {
		t.Errorf("--overwrite left the server with %q", got)
	}

	edited := filepath.Join(home, "edited.json")
	writeConfigFile(t, edited, `{"model":"haiku"}`)
	if err = push("created settings.local.json\n"+
		"created 1, replaced 0, skipped 0 identical, refused 0, left out 0 in ~/.claude (17 bytes uploaded)\n",
		configPushOptions{from: edited}, "settings.local.json"); err != nil {
		t.Fatalf("config push --from: %v", err)
	}
	if got := serverFile("settings.local.json"); got != `{"model":"haiku"}` {
		t.Errorf("--from left the server with %q", got)
	}
	if _, err = os.Stat(filepath.Join(claude, "settings.local.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("--from wrote the agent's directory on this computer: %v", err)
	}
	if err = push("left out auth.json: credential\n"+
		"created 0, replaced 0, skipped 0 identical, refused 0, left out 1 in ~/.claude (0 bytes uploaded)\n",
		configPushOptions{from: edited}, "auth.json"); err != nil {
		t.Fatalf("config push --from to a credential path: %v", err)
	}
	sentBefore := len(*imports)

	if err = push("left out skills/linked/notes.md: symlink skills/linked, not followed\n"+
		"left out link.md: symlink link.md, not followed\n"+
		"left out skills/linked: symlink skills/linked, not followed\n"+
		"created 0, replaced 0, skipped 0 identical, refused 0, left out 3 in ~/.claude (0 bytes uploaded)\n",
		configPushOptions{}, "skills/linked/notes.md", "link.md", "skills/linked"); err != nil {
		t.Fatalf("config push through symlinks: %v", err)
	}

	for _, path := range []string{"../outside.md", filepath.Join(home, "outside.md")} {
		err = configPush(c, &out, "claude", []string{"settings.json", path}, configPushOptions{})
		if err == nil || !strings.Contains(err.Error(), path+" is outside ~/.claude") {
			t.Errorf("push of %s: %v, want an outside-the-directory error", path, err)
		}
	}
	if err = configPush(c, &out, "claude", []string{"missing.md"}, configPushOptions{}); !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "missing.md") {
		t.Errorf("push of a missing path: %v", err)
	}
	if err = configPush(c, &out, "nope", []string{"."}, configPushOptions{}); err == nil || !strings.Contains(err.Error(), `agent "nope"`) || !strings.Contains(err.Error(), "aether config ls") {
		t.Errorf("push for an unknown agent: %v", err)
	}
	if len(*imports) != sentBefore {
		t.Errorf("failed pushes sent %d config.import requests", len(*imports)-sentBefore)
	}
}

func TestConfigPushKeepsADirectoryNamedLikeTheAgentDirectory(t *testing.T) {
	home := testhome.Isolate(t)
	writeConfigFile(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"opus"}`)
	writeConfigFile(t, filepath.Join(home, ".claude", ".claude", "settings.json"), `{"model":"sonnet"}`)
	backend, serverHome := configServer(t)
	c, _ := configControl(t, backend)

	var out bytes.Buffer
	if err := configPush(c, &out, "claude", []string{"settings.json", ".claude"}, configPushOptions{}); err != nil {
		t.Fatalf("config push: %v", err)
	}
	want := "created settings.json\ncreated .claude/settings.json\ncreated 2, replaced 0, skipped 0 identical, refused 0, left out 0 in ~/.claude (34 bytes uploaded)\n"
	if out.String() != want {
		t.Errorf("push output:\n%s\nwant:\n%s", out.String(), want)
	}
	for rel, content := range map[string]string{"settings.json": `{"model":"opus"}`, ".claude/settings.json": `{"model":"sonnet"}`} {
		got, err := os.ReadFile(filepath.Join(serverHome, ".claude", filepath.FromSlash(rel)))
		if err != nil || string(got) != content {
			t.Errorf("server ~/.claude/%s = %q, %v; want %q", rel, got, err, content)
		}
	}

	out.Reset()
	if err := configTree(c, &out, "claude", ".claude", false); err != nil {
		t.Fatalf("config ls claude .claude: %v", err)
	}
	if want = "file\t18\tsettings.json\n"; out.String() != want {
		t.Errorf("config ls claude .claude:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestConfigPushSplitsRequestsAndStopsAtTheFirstFailure(t *testing.T) {
	home := testhome.Isolate(t)
	claude := filepath.Join(home, ".claude")
	var many []string
	for i := range 2002 {
		rel := fmt.Sprintf("many/%04d.md", i)
		many = append(many, rel)
		writeConfigFile(t, filepath.Join(claude, filepath.FromSlash(rel)), "x\n")
	}
	writeConfigFile(t, filepath.Join(claude, "big", "a.md"), strings.Repeat("a", 1<<20))
	writeConfigFile(t, filepath.Join(claude, "big", "b.md"), strings.Repeat("b", 21<<20))
	writeConfigFile(t, filepath.Join(claude, "big", "c.md"), "c\n")
	writeConfigFile(t, filepath.Join(claude, "big", "d.md"), "d\n")

	sent := func(imports []protocol.ConfigImportParams) [][]string {
		requests := make([][]string, 0, len(imports))
		for _, p := range imports {
			paths := make([]string, 0, len(p.Files))
			for _, f := range p.Files {
				paths = append(paths, strings.TrimPrefix(f.Path, ".claude/"))
			}
			requests = append(requests, paths)
		}
		return requests
	}
	lines := func(prefix string, rels []string) string {
		var b strings.Builder
		for _, rel := range rels {
			b.WriteString(prefix + rel + "\n")
		}
		return b.String()
	}

	t.Run("at 2,000 files", func(t *testing.T) {
		backend, _ := configServer(t)
		c, imports := configControl(t, configImportReply{backend, configImported})
		var out bytes.Buffer
		if err := configPush(c, &out, "claude", []string{"many"}, configPushOptions{}); err != nil {
			t.Fatalf("config push many: %v", err)
		}
		if want := lines("created ", many) + "created 2002, replaced 0, skipped 0 identical, refused 0, left out 0 in ~/.claude (4004 bytes uploaded)\n"; out.String() != want {
			t.Errorf("push output ends:\n%s", out.String()[max(0, out.Len()-200):])
		}
		requests := sent(*imports)
		if len(requests) != 2 || !slices.Equal(requests[0], many[:2000]) || !slices.Equal(requests[1], many[2000:]) {
			t.Errorf("requests carry %d files, want 2,000 then 2", len(slices.Concat(requests...)))
		}
		for _, f := range (*imports)[1].Files {
			if !strings.HasPrefix(f.Path, ".claude/many/") {
				t.Errorf("sent path %q lacks the .claude/ prefix", f.Path)
			}
		}
	})

	t.Run("at 20 MiB", func(t *testing.T) {
		backend, _ := configServer(t)
		c, imports := configControl(t, backend)
		var out bytes.Buffer
		if err := configPush(c, &out, "claude", []string{"big"}, configPushOptions{}); err != nil {
			t.Fatalf("config push big: %v", err)
		}
		want := [][]string{{"big/a.md"}, {"big/b.md"}, {"big/c.md", "big/d.md"}}
		if got := sent(*imports); !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("requests %v, want %v", got, want)
		}
		if want := fmt.Sprintf("created 4, replaced 0, skipped 0 identical, refused 0, left out 0 in ~/.claude (%d bytes uploaded)\n", 22<<20+4); !strings.HasSuffix(out.String(), want) {
			t.Errorf("push output:\n%s\nwant it to end with:\n%s", out.String(), want)
		}
	})

	t.Run("partial reply", func(t *testing.T) {
		backend, _ := configServer(t)
		c, imports := configControl(t, configImportReply{backend, func([]memberhome.ConfigFile) (protocol.ConfigImportResult, error) {
			return protocol.ConfigImportResult{
				Harness: "claude", Files: 2, Bytes: 4, Excluded: []protocol.ConfigExcluded{},
				Error: `config import "many/0002.md": no space left on device`, ImportedPaths: many[:2],
			}, nil
		}})
		var out bytes.Buffer
		err := configPush(c, &out, "claude", []string{"many"}, configPushOptions{})
		if err == nil || err.Error() != `upload to ~/.claude stopped: config import "many/0002.md": no space left on device` {
			t.Errorf("push error: %v", err)
		}
		if want := lines("created ", many[:2]); out.String() != want {
			t.Errorf("push output:\n%s\nwant:\n%s", out.String(), want)
		}
		if len(*imports) != 1 {
			t.Errorf("sent %d requests after a failed one, want only the failed one", len(*imports)-1)
		}
	})

	t.Run("lost reply", func(t *testing.T) {
		backend, _ := configServer(t)
		var c *protocol.Client
		c, imports := configControl(t, configImportReply{backend, func([]memberhome.ConfigFile) (protocol.ConfigImportResult, error) {
			_ = c.Close()
			return protocol.ConfigImportResult{}, nil
		}})
		var out bytes.Buffer
		err := configPush(c, &out, "claude", []string{"many"}, configPushOptions{})
		if err == nil || !strings.Contains(err.Error(), "the 2000 unconfirmed files may or may not be on the server") {
			t.Errorf("push error: %v", err)
		}
		if want := lines("unconfirmed ", many[:2000]); out.String() != want {
			t.Errorf("push output does not list exactly the first request's files as unconfirmed:\n%s", out.String()[max(0, out.Len()-200):])
		}
		if len(*imports) != 1 {
			t.Errorf("sent %d requests after a lost reply", len(*imports)-1)
		}
	})

	t.Run("file grown after the walk", func(t *testing.T) {
		grown := filepath.Join(claude, "many", "2001.md")
		t.Cleanup(func() { _ = os.WriteFile(grown, []byte("x\n"), 0o644) })
		backend, _ := configServer(t)
		c, imports := configControl(t, configImportReply{backend, func(files []memberhome.ConfigFile) (protocol.ConfigImportResult, error) {
			if err := os.Truncate(grown, 65<<20); err != nil {
				return protocol.ConfigImportResult{}, err
			}
			return configImported(files)
		}})
		var out bytes.Buffer
		if err := configPush(c, &out, "claude", []string{"many"}, configPushOptions{}); err != nil {
			t.Fatalf("config push many: %v", err)
		}
		want := lines("created ", many[:2000]) + "created many/2000.md\n" +
			"left out many/2001.md: 68157440 bytes is over the 64 MiB upload limit; copy it onto the server from aether terminal\n" +
			"created 2001, replaced 0, skipped 0 identical, refused 0, left out 1 in ~/.claude (4002 bytes uploaded)\n"
		if out.String() != want {
			t.Errorf("push output ends:\n%s", out.String()[max(0, out.Len()-400):])
		}
		if got := sent(*imports); len(got) != 2 || !slices.Equal(got[1], []string{"many/2000.md"}) {
			t.Errorf("second request %v, want only many/2000.md", got[len(got)-1])
		}
	})
}
