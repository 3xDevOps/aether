// Command release-edge writes the image digests and edge section that release-manifest signs.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
	"github.com/3xDevOps/Aether/internal/selfupdate"
)

const upgradePath = "packaging/edge/upgrade.json"

// recordedFiles are the files that shape what an edge host runs, so a
// change to any of them may need a manual step. Sorted.
var recordedFiles = []string{
	"images/edge/Dockerfile",
	"packaging/edge/aether-edge.env.example",
	"packaging/systemd/aether-edge.service",
}

type upgrade struct {
	StoreSchema      int                     `json:"store_schema"`
	StoreCompatFloor int                     `json:"store_compat_floor"`
	Protocol         selfupdate.EdgeProtocol `json:"protocol"`
	// Files maps each of recordedFiles to its SHA-256.
	Files          map[string]string       `json:"files"`
	MinUpgradeFrom string                  `json:"min_upgrade_from"`
	ManualSteps    []selfupdate.ManualStep `json:"manual_steps"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "release-edge:", err)
		os.Exit(1)
	}
}

type imageFlag map[string]string

func (f imageFlag) String() string { return fmt.Sprint(map[string]string(f)) }

func (f imageFlag) Set(v string) error {
	name, digest, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("%q is not <name>=<digest>", v)
	}
	if _, dup := f[name]; dup {
		return fmt.Errorf("image %s is listed twice", name)
	}
	f[name] = digest
	return nil
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("release-edge", flag.ContinueOnError)
	tag := fs.String("tag", "", "release tag")
	images := imageFlag{}
	fs.Var(images, "image", "<name>=<index digest> of a published image; repeat for each")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tag == "" || fs.NArg() != 0 {
		return errors.New("usage: release-edge -tag <tag> -image <name>=<digest> [-image <name>=<digest>]")
	}
	u, err := readUpgrade(".")
	if err != nil {
		return err
	}
	r := selfupdate.ImageRelease{
		Images: images,
		Edge: selfupdate.Edge{
			Image:            selfupdate.EdgeImage,
			ImageDigest:      images["aether-edge"],
			StoreSchema:      u.StoreSchema,
			StoreCompatFloor: u.StoreCompatFloor,
			Protocol:         u.Protocol,
			MinUpgradeFrom:   u.MinUpgradeFrom,
			ManualSteps:      u.ManualSteps,
		},
	}
	if err = r.Validate(*tag); err != nil {
		return err
	}
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", out)
	return err
}

// readUpgrade reads the upgrade record under root and requires every value
// it records to match this build and the packaging files.
func readUpgrade(root string) (upgrade, error) {
	raw, err := os.ReadFile(filepath.Join(root, upgradePath))
	if err != nil {
		return upgrade{}, err
	}
	var u upgrade
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&u); err != nil {
		return upgrade{}, fmt.Errorf("parse %s: %w", upgradePath, err)
	}
	if u.ManualSteps == nil {
		u.ManualSteps = []selfupdate.ManualStep{}
	}
	var stale []string
	record := func(field string, recorded, actual any) {
		if recorded != actual {
			stale = append(stale, fmt.Sprintf("%s is %v, %s records %v", field, actual, upgradePath, recorded))
		}
	}
	record("store_schema", u.StoreSchema, edgestore.SchemaVersion())
	record("store_compat_floor", u.StoreCompatFloor, edgestore.CompatFloor())
	record("protocol", u.Protocol, selfupdate.EdgeProtocol{Version: edgeproto.Version, MinVersion: edgeproto.MinVersion})
	if got := slices.Sorted(maps.Keys(u.Files)); !slices.Equal(got, recordedFiles) {
		stale = append(stale, fmt.Sprintf("%s records files %v, want exactly %v", upgradePath, got, recordedFiles))
	}
	for _, name := range recordedFiles {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return upgrade{}, err
		}
		sum := sha256.Sum256(data)
		record("the SHA-256 of "+name, u.Files[name], hex.EncodeToString(sum[:]))
	}
	if len(stale) > 0 {
		return upgrade{}, fmt.Errorf("%s is out of date: %s; decide whether upgrading across this change needs a manual step or a higher min_upgrade_from (docs/install.md, Edge upgrade record), then record the new values",
			upgradePath, strings.Join(stale, "; "))
	}
	return u, nil
}
