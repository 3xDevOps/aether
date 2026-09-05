package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/macinstall"
)

// release serves body as this platform's asset of v1.3.0 under the
// GitHub release download layout updateBinaries expects.
func release(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	asset := "aether-" + runtime.GOOS + "-" + runtime.GOARCH
	sum := sha256.Sum256(body)
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/download/v1.3.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
	})
	mux.HandleFunc("/releases/download/v1.3.0/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// unwritableBinary lays out a binary in a directory this user cannot
// write, the shape of a root-owned /usr/local/bin, and returns its path.
func unwritableBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the authorized path is refused on Windows before it is reached")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "aether")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dst
}

// authorizedPlatform makes this an administrator-dialog platform for one
// test, with the root-ownership check off because the stub installer
// runs as this user. The staging directory is pointed at a temp dir on
// every platform's idea of the user cache.
func authorizedPlatform(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the authorized path is refused on Windows before it is reached")
	}
	oldAdmin, oldOwner, oldSession, oldPath := adminPlatform, checkRootOwner, hasGUISession, pathIsRootOnly
	t.Cleanup(func() {
		adminPlatform, checkRootOwner, hasGUISession, pathIsRootOnly = oldAdmin, oldOwner, oldSession, oldPath
	})
	adminPlatform, checkRootOwner = true, false
	hasGUISession = func() bool { return true }
	// The test's directories are its own, under a temp root the world can
	// write, so the rule is checked with this user standing in for root
	// and the walk stopping at the directory itself.
	pathIsRootOnly = func(dir string) bool { return ownedOnlyBy(dir, uint32(os.Getuid()), dir) }
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
}

func stubAdmin(t *testing.T, fn adminInstaller) {
	t.Helper()
	old := adminInstall
	t.Cleanup(func() { adminInstall = old })
	adminInstall = fn
}

// copyAsRoot does what the privileged command does, as this user: copies
// the staged file over the destination, mode 0755.
func copyAsRoot(t *testing.T, req macinstall.Request) error {
	t.Helper()
	dir := filepath.Dir(req.Dst)
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	defer func() { _ = os.Chmod(dir, 0o500) }()
	body, err := os.ReadFile(req.Src)
	if err != nil {
		return err
	}
	return os.WriteFile(req.Dst, body, 0o755)
}

func TestUpdateWithAuthorizationAsksWhenUnwritable(t *testing.T) {
	dst := unwritableBinary(t)
	authorizedPlatform(t)
	body := []byte("new binary contents")
	srv := release(t, body)

	var got macinstall.Request
	var prompt string
	stubAdmin(t, func(_ context.Context, req macinstall.Request, p string) error {
		got, prompt = req, p
		if _, err := os.Stat(req.Src); err != nil {
			t.Errorf("staged file: %v", err)
		}
		return copyAsRoot(t, req)
	})

	replaced, err := updateBinaries(t.Context(), srv.URL, "v1.3.0", dst, "aether", nil, adminInstall)
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 1 || replaced[0] != dst {
		t.Fatalf("replaced = %v, want [%s]", replaced, dst)
	}
	sum := sha256.Sum256(body)
	if got.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("request digest = %s, want the release's", got.SHA256)
	}
	if got.Dst != dst {
		t.Fatalf("request dst = %s, want %s", got.Dst, dst)
	}
	if !strings.Contains(got.Src, filepath.Join("aether", "update")) {
		t.Fatalf("request src = %s, want it under the private staging directory", got.Src)
	}
	if _, statErr := os.Stat(got.Src); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staged file after the install: %v, want it removed", statErr)
	}
	if !strings.Contains(prompt, dst) || !strings.Contains(prompt, "v1.3.0") {
		t.Fatalf("prompt = %q, want it to name the binary and the release", prompt)
	}
	installed, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(body) {
		t.Fatalf("dst = %q, want the release", installed)
	}
}

func TestUpdateWithAuthorizationNeverAsksWhenWritable(t *testing.T) {
	authorizedPlatform(t)
	body := []byte("new binary contents")
	srv := release(t, body)
	dst := filepath.Join(t.TempDir(), "aether")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubAdmin(t, func(context.Context, macinstall.Request, string) error {
		t.Fatal("asked for administrator access for a writable directory")
		return nil
	})

	if _, err := updateBinaries(t.Context(), srv.URL, "v1.3.0", dst, "aether", nil, adminInstall); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != string(body) {
		t.Fatalf("dst = %q, want the release", got)
	}
}

func TestUpdateWithoutAuthorizerKeepsTheSudoCommand(t *testing.T) {
	dst := unwritableBinary(t)
	authorizedPlatform(t)
	stubAdmin(t, func(context.Context, macinstall.Request, string) error {
		t.Fatal("Update must never ask for administrator access")
		return nil
	})
	_, err := updateBinaries(t.Context(), "http://unused.invalid", "v1.3.0", dst, "aether", nil, nil)
	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "sudo aether update") {
		t.Fatalf("err = %v, want the permission refusal naming sudo aether update", err)
	}
}

func TestUpdateWithAuthorizationOffMacKeepsTheSudoCommand(t *testing.T) {
	dst := unwritableBinary(t)
	authorizedPlatform(t)
	adminPlatform = false
	stubAdmin(t, func(context.Context, macinstall.Request, string) error {
		t.Fatal("asked for administrator access off the platform that has the dialog")
		return nil
	})
	_, err := updateBinaries(t.Context(), "http://unused.invalid", "v1.3.0", dst, "aether", nil, adminInstall)
	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "sudo aether update") {
		t.Fatalf("err = %v, want the permission refusal naming sudo aether update", err)
	}
}

func TestUpdateWithAuthorizationNeedsAGUISession(t *testing.T) {
	dst := unwritableBinary(t)
	authorizedPlatform(t)
	hasGUISession = func() bool { return false }
	stubAdmin(t, func(context.Context, macinstall.Request, string) error {
		t.Fatal("asked for administrator access with no session to show the dialog in")
		return nil
	})
	_, err := updateBinaries(t.Context(), "http://unused.invalid", "v1.3.0", dst, "aether", nil, adminInstall)
	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "sudo aether update") {
		t.Fatalf("err = %v, want the permission refusal naming sudo aether update", err)
	}
	if got, _ := access(dst); got.Method != MethodManual {
		t.Fatalf("method without a GUI session = %s, want manual", got.Method)
	}
}

// A directory another account can write is not one root should stage a
// file in by name, whoever owns it.
func TestUpdateWithAuthorizationRefusesADirectoryOthersCanWrite(t *testing.T) {
	dst := unwritableBinary(t)
	authorizedPlatform(t)
	// Unwritable by this user (no owner write bit) yet writable by the
	// group: the shape of a shared tools directory.
	if err := os.Chmod(filepath.Dir(dst), 0o570); err != nil {
		t.Fatal(err)
	}
	stubAdmin(t, func(context.Context, macinstall.Request, string) error {
		t.Fatal("asked for administrator access for a directory others can write")
		return nil
	})
	_, err := updateBinaries(t.Context(), "http://unused.invalid", "v1.3.0", dst, "aether", nil, adminInstall)
	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "sudo aether update") {
		t.Fatalf("err = %v, want the permission refusal naming sudo aether update", err)
	}
	if got, _ := access(dst); got.Method != MethodManual {
		t.Fatalf("method for a group-writable directory = %s, want manual", got.Method)
	}
}

func TestUpdateWithAuthorizationRefusesABadTagBeforeAsking(t *testing.T) {
	dst := unwritableBinary(t)
	authorizedPlatform(t)
	stubAdmin(t, func(context.Context, macinstall.Request, string) error {
		t.Fatal("asked with a tag that is not a release")
		return nil
	})
	_, err := updateBinaries(t.Context(), "http://unused.invalid", `v1"; rm -rf /`, dst, "aether", nil, adminInstall)
	if err == nil || !strings.Contains(err.Error(), "not a release tag") {
		t.Fatalf("err = %v, want the tag refused", err)
	}
}

func TestUpdateWithAuthorizationReportsTheDialogsRefusal(t *testing.T) {
	dst := unwritableBinary(t)
	authorizedPlatform(t)
	body := []byte("new binary contents")
	srv := release(t, body)
	stubAdmin(t, func(context.Context, macinstall.Request, string) error {
		return macinstall.ErrCanceled
	})

	replaced, err := updateBinaries(t.Context(), srv.URL, "v1.3.0", dst, "aether", nil, adminInstall)
	if !errors.Is(err, macinstall.ErrCanceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if strings.Contains(err.Error(), "replace ") {
		t.Fatalf("err = %v, want the dialog's line alone: nothing was attempted on the file", err)
	}
	if len(replaced) != 0 {
		t.Fatalf("replaced = %v after a refusal", replaced)
	}
	if got, _ := os.ReadFile(dst); string(got) != "old" {
		t.Fatalf("dst = %q, want it untouched", got)
	}
}

func TestUpdateWithAuthorizationChecksWhatRootLeft(t *testing.T) {
	dst := unwritableBinary(t)
	authorizedPlatform(t)
	body := []byte("new binary contents")
	srv := release(t, body)
	stubAdmin(t, func(_ context.Context, req macinstall.Request, _ string) error {
		// Root reported success but the file is not the release.
		req.Src = filepath.Join(t.TempDir(), "other")
		if err := os.WriteFile(req.Src, []byte("not the release"), 0o600); err != nil {
			return err
		}
		return copyAsRoot(t, req)
	})

	replaced, err := updateBinaries(t.Context(), srv.URL, "v1.3.0", dst, "aether", nil, adminInstall)
	if err == nil || !strings.Contains(err.Error(), "does not match the release checksum") {
		t.Fatalf("err = %v, want the installed file refused", err)
	}
	if len(replaced) != 0 {
		t.Fatalf("replaced = %v, want nothing reported as replaced", replaced)
	}
}

// The rule the production gate applies: every directory from the binary's
// up to the top must be root's alone, with root played by this user.
func TestOwnedOnlyByWalksToTheTop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory modes are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root owns everything")
	}
	top := t.TempDir()
	// t.TempDir inherits the umask; under 002 it comes out group-writable
	// and the walk would refuse `top` itself rather than what the test
	// arranges below.
	if err := os.Chmod(top, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(top, "usr", "local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	if !ownedOnlyBy(dir, uid, top) {
		t.Fatal("a chain of 0755 directories owned by the caller was refused")
	}
	if ownedOnlyBy(dir, uid+1, top) {
		t.Fatal("a chain owned by someone else was accepted")
	}
	// A group-writable ancestor is enough to refuse the whole path.
	if err := os.Chmod(filepath.Join(top, "usr"), 0o775); err != nil {
		t.Fatal(err)
	}
	if ownedOnlyBy(dir, uid, top) {
		t.Fatal("a group-writable ancestor was accepted")
	}
	if err := os.Chmod(filepath.Join(top, "usr"), 0o755); err != nil {
		t.Fatal(err)
	}
	// So is an ancestor that is a symlink, wherever it points.
	real := filepath.Join(top, "real")
	if err := os.Rename(filepath.Join(top, "usr"), real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(top, "usr")); err != nil {
		t.Fatal(err)
	}
	if ownedOnlyBy(dir, uid, top) {
		t.Fatal("a symlinked ancestor was accepted")
	}
	// The production walk goes to the filesystem root; from a temp
	// directory the world can write, that must refuse.
	if ownedOnlyBy(filepath.Join(real, "local", "bin"), uid, "/") {
		t.Fatal("a path under a world-writable temp root was accepted all the way up")
	}
}

func TestPrivateStageDirIsThisUsersAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory modes are not enforced on Windows")
	}
	authorizedPlatform(t)
	dir, err := privateStageDir()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", info.Mode().Perm())
	}
	// A directory that already exists is tightened, not trusted.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := privateStageDir(); err != nil {
		t.Fatal(err)
	}
	if info, _ = os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("mode after reuse = %v, want 0700", info.Mode().Perm())
	}
	// A symlink where the directory should be is refused: root reads
	// from here, and the link could point anywhere.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := privateStageDir(); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v, want the symlink refused", err)
	}
}

func TestAccessSaysHowTheBinaryUpdates(t *testing.T) {
	dst := unwritableBinary(t)
	authorizedPlatform(t)
	got, err := access(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != MethodAdminPrompt || got.Path != dst {
		t.Fatalf("access = %+v, want admin-prompt for %s", got, dst)
	}
	adminPlatform = false
	if got, _ = access(dst); got.Method != MethodManual {
		t.Fatalf("method off macOS = %s, want manual", got.Method)
	}
	writable := filepath.Join(t.TempDir(), "aether")
	if err := os.WriteFile(writable, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ = access(writable); got.Method != MethodDirect {
		t.Fatalf("method for a writable directory = %s, want direct", got.Method)
	}
}

func TestProbeResolvesThisBinary(t *testing.T) {
	got, err := Probe()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got.Path) || got.Method == "" {
		t.Fatalf("probe = %+v, want an absolute path and a method", got)
	}
}
