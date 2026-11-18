package vmimage

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestQemuImgNamesFormatsAndNeverOpensAHeadersBackingFile records every
// qemu-img call the store makes, and fails unless each names the format of
// every image it reads and none opens a backing file an image's header
// names. A header a guest could have written would otherwise pick what the
// server reads.
func TestQemuImgNamesFormatsAndNeverOpensAHeadersBackingFile(t *testing.T) {
	needTools(t, "mkfs.ext4", "qemu-img", "qemu-io")
	real, _ := exec.LookPath("qemu-img")
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\nprintf '%s\\037' \"$@\" >> '" + calls + "'\necho >> '" + calls + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "qemu-img"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := t.Context()
	s := openStore(t, nil)
	dir := t.TempDir()
	base := Base{ID: "sha256:test", Path: filepath.Join(dir, "base.raw")}
	if err := mkfs(ctx, base.Path, 64<<20, "aether-root", ""); err != nil {
		t.Fatal(err)
	}
	root, err := s.NewRootDisk(ctx, base, filepath.Join(dir, "root.img"))
	if err != nil || root.Format != FormatQcow2 {
		t.Fatalf("root disk %+v, %v", root, err)
	}
	if err = s.NewCheckoutDisk(ctx, "k", 64<<20, func(context.Context, string) error { return nil }, filepath.Join(dir, "checkout.qcow2")); err != nil {
		t.Fatal(err)
	}
	if out, ioErr := exec.Command("qemu-io", "-f", "qcow2", "-c", "write -P 0xab 0 512", root.Path).CombinedOutput(); ioErr != nil {
		t.Fatalf("qemu-io: %v: %s", ioErr, out)
	}
	// A copy whose header names a backing file that does not exist: a save
	// that opened it would fail.
	copyPath := filepath.Join(dir, "root-copy.img")
	if err = copyDisk(ctx, root.Path, copyPath); err != nil {
		t.Fatal(err)
	}
	if out, rebaseErr := exec.Command(real, "rebase", "-u", "-f", "qcow2", "-F", "raw", "-b", filepath.Join(dir, "absent"), copyPath).CombinedOutput(); rebaseErr != nil {
		t.Fatalf("qemu-img rebase: %v: %s", rebaseErr, out)
	}
	saved, err := s.SaveBase(ctx, Disk{Path: copyPath, Format: FormatQcow2}, base, "aether/qemu-test:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := head(t, saved.Path, 2048); !bytes.Equal(got[:512], bytes.Repeat([]byte{0xab}, 512)) || !bytes.Equal(got[1024:], head(t, base.Path, 2048)[1024:]) {
		t.Errorf("the saved base is not the copy over the recorded base: starts %x", got[:16])
	}

	// A cache promoted from a qcow2 disk whose header names a link that is
	// gone by then.
	empty, err := s.NewCacheDisk(ctx, "pool", filepath.Join(dir, "empty-cache.img"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SnapshotCache(empty)
	if err == nil {
		err = s.PromoteCache("pool", snapshot)
	}
	if err != nil {
		t.Fatal(err)
	}
	cache, err := s.NewCacheDisk(ctx, "pool", filepath.Join(dir, "cache.img"))
	if err != nil || cache.Format != FormatQcow2 {
		t.Fatalf("cache disk %+v, %v", cache, err)
	}
	if snapshot, err = s.SnapshotCache(cache); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(cache.Backing); err != nil {
		t.Fatal(err)
	}
	if err = s.PromoteCache("pool", snapshot); err != nil {
		t.Fatal(err)
	}

	recorded, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	seen, commits := map[string]bool{}, 0
	for line := range strings.Lines(string(recorded)) {
		args := strings.Split(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\x1f"), "\x1f")
		seen[args[0]] = true
		switch args[0] {
		case "create":
			if !hasPair(args, "-f", "qcow2") || !hasPair(args, "-F", "raw") || !slices.Contains(args, "-u") {
				t.Errorf("qemu-img %v: a create must name both formats and not open its backing file (-u)", args)
			}
		case "commit":
			if !slices.Contains(args, "--image-opts") || !slices.Contains(args, "-d") || slices.Contains(args, "-f") {
				t.Errorf("qemu-img %v: a commit must describe its input with --image-opts and keep it (-d)", args)
				continue
			}
			opts := args[len(args)-1]
			_, backing, _ := strings.Cut(opts, ",backing.file.filename=")
			storeCopy := strings.HasPrefix(backing, s.tmpDir()) && strings.HasSuffix(backing, "/disk.raw")
			if !strings.HasPrefix(opts, "driver=qcow2,") || !strings.Contains(opts, ",backing.driver=raw,") || !storeCopy {
				t.Errorf("qemu-img %v: the input %q must be qcow2 over the store's raw copy of a base it recorded", args, opts)
			}
			commits++
		default:
			t.Errorf("qemu-img %v: a call this test does not check", args)
		}
	}
	if !seen["create"] || commits != 2 {
		t.Errorf("recorded calls %q, want creates and a commit for the save and for the cache", recorded)
	}

	// Every qemu-img call in the module's code is one the store made above.
	sites := qemuImgCalls(t, filepath.Join("..", ".."))
	if want := []string{"vmimage/store.go:create", "vmimage/store.go:commit"}; !slices.Equal(sites, want) {
		t.Errorf("qemu-img is called at %v, want only %v: check every new call in this test", sites, want)
	}
}

func hasPair(args []string, flag, value string) bool {
	i := slices.Index(args, flag)
	return i >= 0 && i+1 < len(args) && args[i+1] == value
}

// qemuImgCalls lists each call in the module's non-test code that passes the
// string "qemu-img", as the file under internal/ and the subcommand.
func qemuImgCalls(t *testing.T, root string) []string {
	t.Helper()
	var sites []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(src, []byte(`"qemu-img"`)) {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for i, arg := range call.Args {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Value == `"qemu-img"` {
					sub := "?"
					if i+1 < len(call.Args) {
						if next, ok := call.Args[i+1].(*ast.BasicLit); ok {
							sub, _ = strconv.Unquote(next.Value)
						}
					}
					rel, _ := filepath.Rel(filepath.Join(root, "internal"), path)
					sites = append(sites, filepath.ToSlash(rel)+":"+sub)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sites
}
