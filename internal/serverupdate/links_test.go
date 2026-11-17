package serverupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/serverunit"
)

const unitText = "[Service]\nExecStart=/usr/local/bin/aether-server serve\n"

func unitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aether-server.service"), []byte(unitText), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// assertHostLayout checks what every host keeps whichever binary runs:
// the real files under the old names holding body, the second names
// linking to them, and the unit's text with its second name linked to it.
func assertHostLayout(t *testing.T, dir, units, cli, server string) {
	t.Helper()
	for path, want := range map[string]string{filepath.Join(dir, "aether"): cli, filepath.Join(dir, "aether-server"): server} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s = %v, %v; want a regular file", path, info, err)
		}
		if got, _ := os.ReadFile(path); string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
	for link, target := range map[string]string{
		filepath.Join(dir, "cloudfield"):        "aether",
		filepath.Join(dir, "cloudfield-server"): "aether-server",
		filepath.Join(units, serverunit.Alias):  "aether-server.service",
	} {
		if got, err := os.Readlink(link); err != nil || got != target {
			t.Fatalf("%s -> %q, %v; want %s", link, got, err, target)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(units, "aether-server.service")); string(got) != unitText {
		t.Fatalf("unit = %q, want its text unchanged", got)
	}
}

// TestServerUpdateFromEitherNameLinksAndRollsBack applies an update to a
// server started as aether-server on a host an older updater left without
// links, and to one started as cloudfield-server: the release lands in the
// real files, the links and the unit link exist, and the rollback after
// the failed re-exec puts the old binaries back under the names the old
// binary and its unit know, with the links still pointing at them.
func TestServerUpdateFromEitherNameLinksAndRollsBack(t *testing.T) {
	for _, start := range []string{"aether-server", "cloudfield-server"} {
		t.Run(start, func(t *testing.T) {
			units := unitDir(t)
			e := newEnvStartedAs(t, false, start, units)
			_, restart, err := e.svc.Update(t.Context(), "mem_admin",
				protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateNow})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			assertHostLayout(t, e.dir, units, newCLIBinary, newServerBinary)
			rec, err := loadRecord(e.state)
			if err != nil || rec.Binaries[0] != e.self || rec.Binaries[1] != e.sibling {
				t.Fatalf("swap record = %+v, %v; want the real files", rec, err)
			}

			restart()
			assertHostLayout(t, e.dir, units, "the old binary", "the old binary")
		})
	}
}

// TestTrialRollbackKeepsTheLinks is the old binary coming back after the
// new one failed its trial, on a host the new one linked: the old files
// return under their names and the links still reach them.
func TestTrialRollbackKeepsTheLinks(t *testing.T) {
	e := newTrialEnv(t)
	units := unitDir(t)
	if err := selfupdate.Link(e.dir); err != nil {
		t.Fatal(err)
	}
	if created, err := (Host{UnitDir: units}).LinkUnit(); err != nil || !created {
		t.Fatalf("LinkUnit = %v, %v; want it created", created, err)
	}
	e.write(t, swapRecord{Boots: maxTrialBoots})
	if _, err := Boot(e.state, e.host); err == nil {
		t.Fatal("the start past the limit did not roll back")
	}
	assertHostLayout(t, e.dir, units, "old cli", "old server")
	for _, link := range []string{"cloudfield", "cloudfield-server"} {
		if _, err := os.Lstat(filepath.Join(e.dir, link) + ".prev"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s.prev: %v, want no copy of a link", link, err)
		}
	}
}

func TestLinkUnitLeavesAnythingElseAtTheName(t *testing.T) {
	units := unitDir(t)
	alias := filepath.Join(units, serverunit.Alias)
	if err := os.WriteFile(alias, []byte("someone else's unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if created, err := (Host{UnitDir: units}).LinkUnit(); err != nil || created {
		t.Fatalf("LinkUnit = %v, %v; want the taken name left without an error", created, err)
	}
	if got, _ := os.ReadFile(alias); string(got) != "someone else's unit" {
		t.Fatalf("%s was replaced: %q", alias, got)
	}
}

func TestLinkUnitWithoutTheUnitLinksNothing(t *testing.T) {
	units := t.TempDir()
	if created, err := (Host{UnitDir: units}).LinkUnit(); err != nil || created {
		t.Fatalf("LinkUnit = %v, %v", created, err)
	}
	if _, err := os.Lstat(filepath.Join(units, serverunit.Alias)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s without the unit: %v", serverunit.Alias, err)
	}
}

// TestALinkThatCannotBeMadeNeverFailsTheUpdate is a filesystem that
// refuses the links: the update is applied all the same.
func TestALinkThatCannotBeMadeNeverFailsTheUpdate(t *testing.T) {
	old := linkBinaries
	t.Cleanup(func() { linkBinaries = old })
	linkBinaries = func([]string) error { return errors.New("symlink: operation not supported") }
	e := newEnv(t, false)
	if _, _, err := e.svc.Update(t.Context(), "mem_admin",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateNow}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := e.read(t, e.self); got != newServerBinary {
		t.Fatalf("aether-server = %q, want the release", got)
	}
	if rec, err := loadRecord(e.state); err != nil || rec.Phase != recordVerifying {
		t.Fatalf("swap record = %+v, %v; want the update applied", rec, err)
	}
}
