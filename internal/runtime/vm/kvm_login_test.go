//go:build kvm

package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// loginFiles are a login a session pins, a share of its own, and one that
// is a plain file of the home.
var loginFiles = []string{".claude/.credentials.json", ".codex/auth.json"}

// readerLogins are where the reader VM finds loginFiles: pinned over its own
// home, as a borrowed login is, and in the owner's home.
var readerLogins = []string{"/root/" + loginFiles[0], "/mnt/owner/" + loginFiles[1]}

// loginVersion is version n of a login. Its size changes with n, so a read
// that trusts a cached size is cut short or runs past the end.
func loginVersion(n int) []byte {
	return fmt.Appendf(nil, `{"version":%d,"pad":"%s"}`, n, strings.Repeat("x", loginPad(n)))
}

func loginPad(n int) int { return 100 + n%7*1000 }

// loginOf returns the version of a whole login, or -1 for a torn one.
func loginOf(data []byte) int {
	var v struct {
		Version int    `json:"version"`
		Pad     string `json:"pad"`
	}
	if json.Unmarshal(data, &v) != nil || len(v.Pad) != loginPad(v.Version) {
		return -1
	}
	return v.Version
}

// readLikeNode reads a file as Node's fs.readFileSync does: as many bytes
// as fstat reports, or to the end when it reports none.
func readLikeNode(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return io.ReadAll(f)
	}
	buf := make([]byte, info.Size())
	n, err := io.ReadFull(f, buf)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = nil
	}
	return buf[:n], err
}

// TestKVMLoginFileNow runs inside a guest, from the test binary.
// AETHER_VM_TEST_LOGIN=write:<first>:<last> replaces every login file in
// the home by rename with versions first to last; read:<last> reads
// readerLogins until each shows last, and counts the reads that were not a
// whole login or went back to an older one.
func TestKVMLoginFileNow(t *testing.T) {
	mode, arg, _ := strings.Cut(os.Getenv("AETHER_VM_TEST_LOGIN"), ":")
	switch mode {
	case "write":
		from, to, _ := strings.Cut(arg, ":")
		first, _ := strconv.Atoi(from)
		last, _ := strconv.Atoi(to)
		for n := first; n <= last; n++ {
			for _, rel := range loginFiles {
				p := filepath.Join("/root", rel)
				if err := os.WriteFile(p+".new", loginVersion(n), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(p+".new", p); err != nil {
					t.Fatal(err)
				}
			}
		}
	case "read":
		last, _ := strconv.Atoi(arg)
		reads, torn, back, versions := 0, 0, 0, 0
		seen := make([]int, len(readerLogins))
		var sample []byte
		for deadline := time.Now().Add(time.Minute); slices.Min(seen) < last && time.Now().Before(deadline); {
			for i, p := range readerLogins {
				data, err := readLikeNode(p)
				if err != nil {
					t.Fatal(err)
				}
				reads++
				switch v := loginOf(data); {
				case v < 0:
					torn++
					sample = data[:min(len(data), 80)]
				case v < seen[i]:
					back++
				case v > seen[i]:
					versions++
					seen[i] = v
				}
			}
		}
		fmt.Printf("login-reader: reads=%d torn=%d back=%d versions=%d last=%d\n", reads, torn, back, versions, slices.Min(seen))
		if sample != nil {
			fmt.Printf("login-reader: a torn read began %q\n", sample)
		}
	default:
		t.Skip("runs inside a guest only")
	}
}

// TestKVMLoginFilesAreCurrentAcrossVMs has one VM replace login files by
// rename while another, which mounts the same home, reads them.
func TestKVMLoginFilesAreCurrentAcrossVMs(t *testing.T) {
	e := kvmEngine(t)
	src := filepath.Join(e.root, "..", "src-login")
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	owner, reader := filepath.Join(src, "owner"), filepath.Join(src, "reader")
	files := map[string]string{}
	for _, rel := range loginFiles {
		files[rel] = string(loginVersion(0))
	}
	writeHome(t, owner, files)
	writeHome(t, reader, map[string]string{loginFiles[0]: ""})
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := runtime.Mount{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true}
	// The writer pins nothing, so each rename replaces the host's file at
	// once; whatever else the reader sees comes from its own caches. The
	// reader's pin sits over a file of its own home, which no rename
	// replaces, so it stays mounted.
	kvmUserSession(t, e, "run-vmlogin-w", runtime.Mount{HostPath: owner, ContainerPath: "/root"}, bin)
	kvmUserSession(t, e, "run-vmlogin-r", runtime.Mount{HostPath: reader, ContainerPath: "/root"},
		runtime.Mount{HostPath: owner, Subpath: loginFiles[0], ContainerPath: readerLogins[0]},
		runtime.Mount{HostPath: owner, ContainerPath: "/mnt/owner"}, bin)
	helper := func(key, env string) string {
		out, code := vmRun(t, e, key, "AETHER_VM_TEST_LOGIN="+env+" /opt/aether-vmtest -test.run '^TestKVMLoginFileNow$' -test.v 2>&1")
		if code != 0 {
			t.Fatalf("%s in %s: %d %s", env, key, code, out)
		}
		return out
	}
	read := "cat " + readerLogins[0] + "; echo; cat " + readerLogins[1]

	// The reader read each login just before the writer replaced it.
	const rounds = 20
	stale := 0
	for n := 1; n <= rounds; n++ {
		vmRun(t, e, "run-vmlogin-r", read)
		helper("run-vmlogin-w", fmt.Sprintf("write:%d:%d", n, n))
		if out, _ := vmRun(t, e, "run-vmlogin-r", read); strings.Count(out, string(loginVersion(n))) != len(loginFiles) {
			stale++
		}
	}
	t.Logf("reads right after another VM replaced the logins that were not the new ones: %d of %d", stale, rounds)

	const first, last = 100, 2000
	if out, code := vmRun(t, e, "run-vmlogin-w", fmt.Sprintf("AETHER_VM_TEST_LOGIN=write:%d:%d setsid /opt/aether-vmtest -test.run '^TestKVMLoginFileNow$' </dev/null >/tmp/writer.log 2>&1 &", first, last)); code != 0 {
		t.Fatalf("start the writer: %d %q", code, out)
	}
	out := helper("run-vmlogin-r", "read:"+strconv.Itoa(last))
	i := strings.Index(out, "login-reader: ")
	if i < 0 {
		t.Fatalf("the reader reported nothing: %s", out)
	}
	t.Log(strings.TrimSpace(out[i:]))
	var reads, torn, back, versions, seen int
	if _, err := fmt.Sscanf(out[i:], "login-reader: reads=%d torn=%d back=%d versions=%d last=%d", &reads, &torn, &back, &versions, &seen); err != nil {
		t.Fatal(err)
	}
	if versions < 10 {
		t.Errorf("the reader saw %d versions: the writer was done before it began", versions)
	}
	if stale > 0 || torn > 0 || back > 0 || seen < last {
		log, _ := vmRun(t, e, "run-vmlogin-w", "cat /tmp/writer.log")
		t.Errorf("stale %d of %d after a replace; while replaced: %d torn and %d older reads of %d, last version seen %d of %d; the writer: %s", stale, rounds, torn, back, reads, seen, last, log)
	}
	if out, _ := vmRun(t, e, "run-vmlogin-r", "grep -c ' "+readerLogins[0]+" ' /proc/mounts"); strings.TrimSpace(out) != "1" {
		t.Errorf("the reader's pinned login is no longer mounted: %q", out)
	}
}
