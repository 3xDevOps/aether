package sshd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type homeEnv struct {
	*testEnv
	client *protocol.Client
	home   string
}

func newHomeEnv(t *testing.T) homeEnv {
	t.Helper()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) { c.Homes = homes })
	home, err := homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	return homeEnv{testEnv: e, client: controlClient(t, e), home: home}
}

func (e homeEnv) seed(t *testing.T, rel, content string) {
	t.Helper()
	dest := filepath.Join(e.home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e homeEnv) tree(t *testing.T, rel string) map[string]protocol.HomeTreeEntry {
	t.Helper()
	var result protocol.HomeTreeResult
	if err := e.client.Call(protocol.MethodHomeTree, protocol.HomePathParams{Path: rel}, &result); err != nil {
		t.Fatalf("home.tree %q: %v", rel, err)
	}
	out := make(map[string]protocol.HomeTreeEntry, len(result.Entries))
	for i, entry := range result.Entries {
		if i > 0 && result.Entries[i-1].Name >= entry.Name {
			t.Fatalf("home.tree %q is not sorted: %+v", rel, result.Entries)
		}
		out[entry.Name] = entry
	}
	return out
}

func wantHomeError(t *testing.T, err error, code int, message string) {
	t.Helper()
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != code || pe.Message != message {
		t.Fatalf("error = %v, want code %d %q", err, code, message)
	}
}

func TestHomeRPCFileLifecycle(t *testing.T) {
	t.Parallel()
	e := newHomeEnv(t)
	c := e.client
	binary := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}

	if err := c.Call(protocol.MethodHomeUpload, protocol.HomeUploadParams{Path: "notes/img/logo.png", Content: binary}, nil); err != nil {
		t.Fatalf("home.upload: %v", err)
	}
	err := c.Call(protocol.MethodHomeUpload, protocol.HomeUploadParams{Path: "notes/img/logo.png", Content: []byte("new")}, nil)
	wantHomeError(t, err, protocol.CodeConflict, "notes/img/logo.png already exists")
	var download protocol.HomeDownloadResult
	if err = c.Call(protocol.MethodHomeDownload, protocol.HomePathParams{Path: "notes/img/logo.png"}, &download); err != nil {
		t.Fatalf("home.download: %v", err)
	}
	if string(download.Content) != string(binary) || download.Size != int64(len(binary)) {
		t.Fatalf("home.download = %+v", download)
	}
	if err = c.Call(protocol.MethodHomeUpload, protocol.HomeUploadParams{Path: "notes/img/logo.png", Content: []byte("new"), Overwrite: true}, nil); err != nil {
		t.Fatalf("home.upload overwrite: %v", err)
	}
	e.seed(t, "big.bin", "")
	if err = os.Truncate(filepath.Join(e.home, "big.bin"), memberhome.ConfigMaxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	err = c.Call(protocol.MethodHomeDownload, protocol.HomePathParams{Path: "big.bin"}, nil)
	wantHomeError(t, err, protocol.CodeInvalidParams, "big.bin is over the 64 MiB limit")
	if err = c.Call(protocol.MethodHomeRemove, protocol.HomePathParams{Path: "big.bin"}, nil); err != nil {
		t.Fatalf("home.remove big.bin: %v", err)
	}

	var read protocol.ConfigFileReadResult
	if err = c.Call(protocol.MethodHomeWrite, protocol.HomeWriteParams{Path: "notes/todo.md", Content: "one\n"}, &read); err != nil {
		t.Fatalf("home.write create: %v", err)
	}
	err = c.Call(protocol.MethodHomeWrite, protocol.HomeWriteParams{Path: "notes/todo.md", Content: "again\n"}, nil)
	wantHomeError(t, err, protocol.CodeConflict, "notes/todo.md already exists")
	err = c.Call(protocol.MethodHomeWrite, protocol.HomeWriteParams{Path: "notes/todo.md", Content: "again\n", Revision: "stale"}, nil)
	wantHomeError(t, err, protocol.CodeConflict, "notes/todo.md changed on the server; reload it and try again")
	if err = c.Call(protocol.MethodHomeWrite, protocol.HomeWriteParams{Path: "notes/todo.md", Content: "two\n", Revision: read.Revision}, &read); err != nil {
		t.Fatalf("home.write save: %v", err)
	}
	if err = c.Call(protocol.MethodHomeRead, protocol.HomePathParams{Path: "notes/todo.md"}, &read); err != nil || read.Content != "two\n" || !read.Writable {
		t.Fatalf("home.read = %+v, %v", read, err)
	}

	if err = c.Call(protocol.MethodHomeMkdir, protocol.HomePathParams{Path: "notes/archive"}, nil); err != nil {
		t.Fatalf("home.mkdir: %v", err)
	}
	err = c.Call(protocol.MethodHomeMkdir, protocol.HomePathParams{Path: "notes/archive"}, nil)
	wantHomeError(t, err, protocol.CodeConflict, "notes/archive already exists")
	err = c.Call(protocol.MethodHomeMkdir, protocol.HomePathParams{Path: "missing/child"}, nil)
	wantHomeError(t, err, protocol.CodeNotFound, "missing was not found")

	err = c.Call(protocol.MethodHomeRename, protocol.HomeRenameParams{From: "notes/todo.md", To: "notes/img/logo.png"}, nil)
	wantHomeError(t, err, protocol.CodeConflict, "notes/img/logo.png already exists")
	err = c.Call(protocol.MethodHomeRename, protocol.HomeRenameParams{From: "notes", To: "notes/archive/notes"}, nil)
	wantHomeError(t, err, protocol.CodeDenied, "notes cannot be moved into itself")
	err = c.Call(protocol.MethodHomeRename, protocol.HomeRenameParams{From: "notes/gone", To: "notes/here"}, nil)
	wantHomeError(t, err, protocol.CodeNotFound, "notes/gone was not found")
	if err = c.Call(protocol.MethodHomeRename, protocol.HomeRenameParams{From: "notes/todo.md", To: "notes/archive/done.md"}, nil); err != nil {
		t.Fatalf("home.rename: %v", err)
	}
	if got := e.tree(t, "notes/archive"); len(got) != 1 || got["done.md"].Kind != "file" {
		t.Fatalf("notes/archive = %+v", got)
	}

	err = c.Call(protocol.MethodHomeRemove, protocol.HomePathParams{Path: "."}, nil)
	wantHomeError(t, err, protocol.CodeDenied, "the home itself cannot be deleted")
	if err = c.Call(protocol.MethodHomeRemove, protocol.HomePathParams{Path: "notes"}, nil); err != nil {
		t.Fatalf("home.remove: %v", err)
	}
	if got := e.tree(t, ""); len(got) != 0 {
		t.Fatalf("home after remove = %+v", got)
	}
	err = c.Call(protocol.MethodHomeRemove, protocol.HomePathParams{Path: "notes"}, nil)
	wantHomeError(t, err, protocol.CodeNotFound, "notes was not found")

	for _, bad := range []string{"/etc/passwd", "../other", "a//b", "a/./b", `a\b`, "a\x00b"} {
		var pe *protocol.Error
		if err = c.Call(protocol.MethodHomeRead, protocol.HomePathParams{Path: bad}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
			t.Errorf("home.read %q = %v, want CodeDenied", bad, err)
		}
	}
}

func TestHomeRPCListsButNeverReadsCredentials(t *testing.T) {
	t.Parallel()
	e := newHomeEnv(t)
	c := e.client
	locked := []string{
		".claude/.credentials.json", ".codex/auth.json", ".config/gh/hosts.yml",
		".git-credentials", ".netrc", ".ssh/id_ed25519", ".ssh/config", "keys/server.pem",
	}
	for _, rel := range locked {
		e.seed(t, rel, "secret")
	}
	e.seed(t, ".ssh/id_ed25519.pub", "ssh-ed25519 public")
	e.seed(t, ".claude/projects/session.jsonl", "{}")

	for _, rel := range locked {
		dir, name := filepath.Split(rel)
		if entry := e.tree(t, strings.TrimSuffix(dir, "/"))[name]; entry.Kind != "file" || !entry.Locked {
			t.Errorf("home.tree entry for %s = %+v, want a locked file", rel, entry)
		}
		wantHomeError(t, c.Call(protocol.MethodHomeRead, protocol.HomePathParams{Path: rel}, nil),
			protocol.CodeDenied, ""+rel+" is a credential file; read it in the Environment terminal")
		wantHomeError(t, c.Call(protocol.MethodHomeDownload, protocol.HomePathParams{Path: rel}, nil),
			protocol.CodeDenied, ""+rel+" is a credential file; read it in the Environment terminal")
		wantHomeError(t, c.Call(protocol.MethodHomeWrite, protocol.HomeWriteParams{Path: rel, Content: "x"}, nil),
			protocol.CodeDenied, ""+rel+" is a credential file; edit it in the Environment terminal")
	}
	if entry := e.tree(t, ".ssh")["id_ed25519.pub"]; entry.Locked {
		t.Fatalf("public key is locked: %+v", entry)
	}
	if entry := e.tree(t, ".claude/projects")["session.jsonl"]; entry.Kind != "file" || entry.Locked {
		t.Fatalf("runtime file = %+v, want listed and readable", entry)
	}

	if err := c.Call(protocol.MethodHomeUpload, protocol.HomeUploadParams{Path: ".netrc", Content: []byte("replaced"), Overwrite: true}, nil); err != nil {
		t.Fatalf("home.upload over a credential: %v", err)
	}
	if err := c.Call(protocol.MethodHomeRename, protocol.HomeRenameParams{From: ".netrc", To: ".netrc.bak"}, nil); err != nil {
		t.Fatalf("home.rename a credential: %v", err)
	}
	if err := c.Call(protocol.MethodHomeRemove, protocol.HomePathParams{Path: ".git-credentials"}, nil); err != nil {
		t.Fatalf("home.remove a credential: %v", err)
	}
}

func TestHomeRPCNeverFollowsLinks(t *testing.T) {
	t.Parallel()
	e := newHomeEnv(t)
	c := e.client
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.seed(t, "real.txt", "inside")
	for name, target := range map[string]string{"file-link": secret, "dir-link": outside} {
		if err := os.Symlink(target, filepath.Join(e.home, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(e.home, "real.txt"), filepath.Join(e.home, "hard.txt")); err != nil {
		t.Fatal(err)
	}

	root := e.tree(t, "")
	if root["file-link"].Kind != "link" || root["dir-link"].Kind != "link" || root["hard.txt"].Kind != "file" {
		t.Fatalf("home.tree = %+v", root)
	}
	for _, call := range []struct {
		method string
		params any
	}{
		{protocol.MethodHomeRead, protocol.HomePathParams{Path: "file-link"}},
		{protocol.MethodHomeRead, protocol.HomePathParams{Path: "hard.txt"}},
		{protocol.MethodHomeRead, protocol.HomePathParams{Path: "dir-link/secret.txt"}},
		{protocol.MethodHomeDownload, protocol.HomePathParams{Path: "file-link"}},
		{protocol.MethodHomeDownload, protocol.HomePathParams{Path: "dir-link/secret.txt"}},
		{protocol.MethodHomeTree, protocol.HomePathParams{Path: "dir-link"}},
		{protocol.MethodHomeWrite, protocol.HomeWriteParams{Path: "file-link", Content: "x"}},
		{protocol.MethodHomeWrite, protocol.HomeWriteParams{Path: "dir-link/new.txt", Content: "x"}},
		{protocol.MethodHomeUpload, protocol.HomeUploadParams{Path: "file-link", Content: []byte("x"), Overwrite: true}},
		{protocol.MethodHomeUpload, protocol.HomeUploadParams{Path: "dir-link/new.txt", Content: []byte("x")}},
		{protocol.MethodHomeMkdir, protocol.HomePathParams{Path: "dir-link/new"}},
		{protocol.MethodHomeRemove, protocol.HomePathParams{Path: "dir-link/secret.txt"}},
		{protocol.MethodHomeRename, protocol.HomeRenameParams{From: "real.txt", To: "dir-link/real.txt"}},
	} {
		var pe *protocol.Error
		if err := c.Call(call.method, call.params, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
			t.Errorf("%s %+v = %v, want CodeDenied", call.method, call.params, err)
		}
	}

	if err := c.Call(protocol.MethodHomeRename, protocol.HomeRenameParams{From: "file-link", To: "moved-link"}, nil); err != nil {
		t.Fatalf("home.rename a symlink: %v", err)
	}
	for _, name := range []string{"moved-link", "dir-link"} {
		if err := c.Call(protocol.MethodHomeRemove, protocol.HomePathParams{Path: name}, nil); err != nil {
			t.Fatalf("home.remove symlink %s: %v", name, err)
		}
	}
	if content, err := os.ReadFile(secret); err != nil || string(content) != "outside" {
		t.Fatalf("symlink target = %q, %v; want it untouched", content, err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 {
		t.Fatalf("outside directory = %v, %v; want only the secret", entries, err)
	}
}

func TestHomeRPCRequiresLaunchAndOwnHome(t *testing.T) {
	t.Parallel()
	e := newHomeEnv(t)
	bobSigner, bob := addMember(t, e.testEnv, "Bob", domain.RoleCollaborator, false)
	bobHome, err := e.srv.cfg.Homes.Path(bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bobHome, "bob.txt"), []byte("bob"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = e.client.Call(protocol.MethodHomeRead, map[string]any{"member_id": bob.ID, "path": "bob.txt"}, nil)
	wantHomeError(t, err, protocol.CodeNotFound, "bob.txt was not found")
	var read protocol.ConfigFileReadResult
	if err = controlAs(t, e.testEnv, bobSigner).Call(protocol.MethodHomeRead, protocol.HomePathParams{Path: "bob.txt"}, &read); err != nil || read.Content != "bob" {
		t.Fatalf("Bob's own home.read = %+v, %v", read, err)
	}

	viewerSigner, _ := addMember(t, e.testEnv, "Viewer", domain.RoleViewer, false)
	viewer := controlAs(t, e.testEnv, viewerSigner)
	for _, method := range []string{
		protocol.MethodHomeTree, protocol.MethodHomeRead, protocol.MethodHomeWrite, protocol.MethodHomeMkdir,
		protocol.MethodHomeRename, protocol.MethodHomeRemove, protocol.MethodHomeUpload, protocol.MethodHomeDownload,
	} {
		var pe *protocol.Error
		if err := viewer.Call(method, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
			t.Errorf("viewer %s = %v, want CodeDenied", method, err)
		}
	}
}
