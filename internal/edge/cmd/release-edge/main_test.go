package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// repoRoot is the repository root, relative to this package.
const repoRoot = "../../../.."

// TestUpgradeRecordMatchesTheCode fails when a change moves the edge store
// schema, compat floor or protocol, or edits a packaging file an operator
// copies, without packaging/edge/upgrade.json recording it. Recording it is
// the moment to decide whether the upgrade needs a manual step.
func TestUpgradeRecordMatchesTheCode(t *testing.T) {
	u, err := readUpgrade(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	edge := selfupdate.Edge{
		Image: selfupdate.EdgeImage, ImageDigest: "sha256:" + strings.Repeat("a", 64),
		StoreSchema: u.StoreSchema, StoreCompatFloor: u.StoreCompatFloor, Protocol: u.Protocol,
		MinUpgradeFrom: u.MinUpgradeFrom, ManualSteps: u.ManualSteps,
	}
	newest := u.MinUpgradeFrom
	for _, s := range u.ManualSteps {
		if selfupdate.Behind(newest, s.Since) {
			newest = s.Since
		}
	}
	if err := edge.Validate(newest); err != nil {
		t.Fatalf("%s: %v", upgradePath, err)
	}
}

func TestReadUpgradeNamesWhatIsStale(t *testing.T) {
	root := t.TempDir()
	for _, name := range append([]string{upgradePath}, recordedFiles...) {
		data, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	unit := filepath.Join(root, "packaging/systemd/aether-edge.service")
	if err := os.WriteFile(unit, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readUpgrade(root)
	if err == nil || !strings.Contains(err.Error(), "the SHA-256 of packaging/systemd/aether-edge.service is") {
		t.Fatalf("err = %v, want the changed unit named", err)
	}
}

func TestRunWritesWhatReleaseManifestSigns(t *testing.T) {
	t.Chdir(repoRoot)
	digest := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	var out bytes.Buffer
	err := run([]string{"-tag", "v99.0.0",
		"-image", "aether-edge=" + digest("e"), "-image", "aether-standard=" + digest("5"),
		"-image", "aether-browser=" + digest("b")}, &out)
	if err != nil {
		t.Fatal(err)
	}
	var r selfupdate.ImageRelease
	if err = json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Edge.ImageDigest != digest("e") || r.Edge.Image != selfupdate.EdgeImage || r.Edge.ManualSteps == nil {
		t.Fatalf("edge = %+v", r.Edge)
	}

	err = run([]string{"-tag", "v99.0.0", "-image", "aether-edge=" + digest("e")}, &out)
	if err == nil || !strings.Contains(err.Error(), "want exactly") {
		t.Fatalf("err = %v, want the missing images refused", err)
	}
}
