package selfupdate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows gets cloudfield.cmd, not symlinks")
	}
}

func writeBinaries(t *testing.T, dir, body string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// assertLayout checks the one layout every host keeps: the real files
// under the old names, holding body, and the second names as relative
// links to them.
func assertLayout(t *testing.T, dir, body string) {
	t.Helper()
	for bin, link := range map[string]string{"aether": "cloudfield", "aether-server": "cloudfield-server"} {
		info, err := os.Lstat(filepath.Join(dir, bin))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s = %v, %v; want a regular file", bin, info, err)
		}
		if got, err := os.ReadFile(filepath.Join(dir, bin)); err != nil || string(got) != body {
			t.Fatalf("%s = %q, %v; want %q", bin, got, err, body)
		}
		if target, err := os.Readlink(filepath.Join(dir, link)); err != nil || target != bin {
			t.Fatalf("%s -> %q, %v; want a link to %s", link, target, err, bin)
		}
	}
}

func TestLinkGivesEachBinaryItsSecondName(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	writeBinaries(t, dir, "bin", "aether", "aether-server")
	for range 2 {
		if err := Link(dir); err != nil {
			t.Fatal(err)
		}
	}
	assertLayout(t, dir, "bin")
}

func TestLinkSkipsABinaryThatIsNotThere(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	writeBinaries(t, dir, "bin", "aether")
	if err := Link(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "cloudfield-server")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cloudfield-server without aether-server: %v", err)
	}
}

// TestLinkNeverReplacesWhatHoldsTheName covers every kind of entry at a
// second name that this code did not make: each is left exactly as it was
// and is no error, because an update must not fail over it.
func TestLinkNeverReplacesWhatHoldsTheName(t *testing.T) {
	skipOnWindows(t)
	for name, setup := range map[string]func(t *testing.T, dir, path string){
		"another program": func(t *testing.T, _, path string) {
			if err := os.WriteFile(path, []byte("someone else's cloudfield"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"a dangling link": func(t *testing.T, _, path string) {
			if err := os.Symlink("gone", path); err != nil {
				t.Fatal(err)
			}
		},
		"a link elsewhere": func(t *testing.T, dir, path string) {
			writeBinaries(t, dir, "other", "other")
			if err := os.Symlink("other", path); err != nil {
				t.Fatal(err)
			}
		},
		"a directory": func(t *testing.T, _, path string) {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeBinaries(t, dir, "bin", "aether")
			path := filepath.Join(dir, "cloudfield")
			setup(t, dir, path)
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			target, _ := os.Readlink(path)

			if err = Link(dir); err != nil {
				t.Fatalf("Link: %v", err)
			}
			if _, err = Symlink(dir, "aether", "cloudfield"); !errors.Is(err, ErrNameTaken) {
				t.Fatalf("Symlink = %v, want ErrNameTaken", err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || after.Mode() != before.Mode() {
				t.Fatalf("%s was replaced: %v, %v", path, after, err)
			}
			if got, _ := os.Readlink(path); got != target {
				t.Fatalf("link now points at %q, want %q", got, target)
			}
		})
	}
}

func TestLinkKeepsAnAbsoluteLinkToTheBinary(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	writeBinaries(t, dir, "bin", "aether")
	abs := filepath.Join(dir, "aether")
	if err := os.Symlink(abs, filepath.Join(dir, "cloudfield")); err != nil {
		t.Fatal(err)
	}
	if created, err := Symlink(dir, "aether", "cloudfield"); err != nil || created {
		t.Fatalf("Symlink over our own absolute link = %v, %v; want it kept", created, err)
	}
	if got, _ := os.Readlink(filepath.Join(dir, "cloudfield")); got != abs {
		t.Fatalf("link = %q, want it kept at %q", got, abs)
	}
}

func TestLinkNamesTheCommandItMayNotRun(t *testing.T) {
	skipOnWindows(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes any directory")
	}
	dir := t.TempDir()
	writeBinaries(t, dir, "bin", "aether", "aether-server")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := Link(dir)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Link = %v, want the permission error", err)
	}
	want := "\nrun: sudo ln -s aether " + filepath.Join(dir, "cloudfield") +
		" && sudo ln -s aether-server " + filepath.Join(dir, "cloudfield-server")
	if !strings.HasSuffix(err.Error(), want) || strings.Count(err.Error(), "run: ") != 1 {
		t.Fatalf("Link = %q, want one line ending %q", err, want)
	}
}

func TestWindowsShimRunsAetherBesideIt(t *testing.T) {
	dir := t.TempDir()
	if err := writeShim(dir, "aether.exe", "cloudfield.cmd"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "cloudfield.cmd"))
	if err != nil || string(got) != "@\"%~dp0aether.exe\" %*\r\n" {
		t.Fatalf("cloudfield.cmd = %q, %v", got, err)
	}
	if err := writeShim(dir, "aether.exe", "cloudfield.cmd"); err != nil {
		t.Fatalf("writeShim over its own shim: %v", err)
	}

	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".cloudfield.cmd.*")); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
	empty := filepath.Join(dir, "empty.cmd")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeShim(dir, "aether.exe", "empty.cmd"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("writeShim over an empty file = %v, want ErrNameTaken", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeShim(dir, "aether.exe", "dir.cmd"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("writeShim over a directory = %v, want ErrNameTaken", err)
	}

	other := filepath.Join(dir, "other.cmd")
	if err := os.WriteFile(other, []byte("@echo someone else's\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeShim(dir, "aether.exe", "other.cmd"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("writeShim over another file = %v, want ErrNameTaken", err)
	}
	if got, _ := os.ReadFile(other); string(got) != "@echo someone else's\r\n" {
		t.Fatalf("other.cmd was replaced: %q", got)
	}
}

// TestUpdateFromEitherNameWritesTheRealFiles starts the update from the
// old name and from the new one, on a host whose links an earlier run
// made and on one an older updater left without them: the release lands
// in the real files, never in a link, and the Link every caller runs
// after it completes the layout.
func TestUpdateFromEitherNameWritesTheRealFiles(t *testing.T) {
	skipOnWindows(t)
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	_, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{
		"aether" + suffix:        []byte("new"),
		"aether-server" + suffix: []byte("new"),
	})
	for _, start := range []string{"aether", "cloudfield", "aether-server", "cloudfield-server"} {
		for _, linked := range []bool{false, true} {
			if !linked && strings.HasPrefix(start, "cloudfield") {
				continue
			}
			dir := t.TempDir()
			writeBinaries(t, dir, "old", "aether", "aether-server")
			if linked {
				if err := Link(dir); err != nil {
					t.Fatal(err)
				}
			}
			primary, companion := "aether", "aether-server"
			if strings.HasSuffix(start, "-server") {
				primary, companion = companion, primary
			}
			replaced, err := UpdateBinaries(t.Context(), srv.URL, "v1.3.0", filepath.Join(dir, start), primary, companion)
			if err != nil {
				t.Fatalf("update from %s: %v", start, err)
			}
			want := []string{filepath.Join(dir, primary), filepath.Join(dir, companion)}
			if strings.Join(replaced, " ") != strings.Join(want, " ") {
				t.Fatalf("update from %s replaced %v, want %v", start, replaced, want)
			}
			if err = Link(dir); err != nil {
				t.Fatal(err)
			}
			assertLayout(t, dir, "new")
		}
	}
}

// TestUpdateKeepsACompanionThatIsALink is the host the layout exists to
// avoid, where aether is a link to the file under the new name: the
// update replaces the file the link resolves to, so the link stays a link
// and nothing is left stale.
func TestUpdateKeepsACompanionThatIsALink(t *testing.T) {
	skipOnWindows(t)
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	_, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{
		"aether" + suffix:        []byte("new cli"),
		"aether-server" + suffix: []byte("new server"),
	})
	dir := t.TempDir()
	writeBinaries(t, dir, "old", "aether-server", "cloudfield")
	if err := os.Symlink("cloudfield", filepath.Join(dir, "aether")); err != nil {
		t.Fatal(err)
	}

	if _, err := UpdateBinaries(t.Context(), srv.URL, "v1.3.0", filepath.Join(dir, "aether-server"), "aether-server", "aether"); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "aether")); err != nil || target != "cloudfield" {
		t.Fatalf("aether -> %q, %v; want the link kept", target, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "cloudfield")); string(got) != "new cli" {
		t.Fatalf("cloudfield = %q, want the release", got)
	}
}

func TestSymlinkReportsWhetherItCreated(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	writeBinaries(t, dir, "bin", "aether")
	for _, want := range []bool{true, false} {
		if created, err := Symlink(dir, "aether", "cloudfield"); err != nil || created != want {
			t.Fatalf("Symlink = %v, %v; want created %v", created, err, want)
		}
	}
}

// TestUpdateProbesTheDirectoryALinkedCompanionResolvesTo is a companion
// linked into a directory this user may not write: the update stops
// before downloading, with the command that can.
func TestUpdateProbesTheDirectoryALinkedCompanionResolvesTo(t *testing.T) {
	skipOnWindows(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes any directory")
	}
	_, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{})
	dir, elsewhere := t.TempDir(), t.TempDir()
	writeBinaries(t, dir, "old", "aether")
	writeBinaries(t, elsewhere, "old", "aether-server")
	if err := os.Symlink(filepath.Join(elsewhere, "aether-server"), filepath.Join(dir, "aether-server")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(elsewhere, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(elsewhere, 0o755) })

	_, err := UpdateBinaries(t.Context(), srv.URL, "v1.3.0", filepath.Join(dir, "aether"), "aether", "aether-server")
	if !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), elsewhere+" is not writable by this user; re-run as `sudo aether update`") {
		t.Fatalf("UpdateBinaries = %v, want the unwritable directory and the sudo command", err)
	}
}

func TestLinkAllLinksEachDirectoryOnce(t *testing.T) {
	skipOnWindows(t)
	one, two := t.TempDir(), t.TempDir()
	writeBinaries(t, one, "bin", "aether")
	writeBinaries(t, two, "bin", "aether-server")
	if err := LinkAll([]string{filepath.Join(one, "aether"), filepath.Join(two, "aether-server"), filepath.Join(one, "aether")}); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{filepath.Join(one, "cloudfield"): "aether", filepath.Join(two, "cloudfield-server"): "aether-server"} {
		if got, err := os.Readlink(link); err != nil || got != target {
			t.Fatalf("%s -> %q, %v; want %s", link, got, err, target)
		}
	}
}

func TestLinkWritesTheShimOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	dir := t.TempDir()
	writeBinaries(t, dir, "bin", "aether.exe")
	for range 2 {
		if err := Link(dir); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(dir, "cloudfield.cmd")); err != nil || string(got) != "@\"%~dp0aether.exe\" %*\r\n" {
		t.Fatalf("cloudfield.cmd = %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "cloudfield")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a symlink on Windows: %v", err)
	}
}
