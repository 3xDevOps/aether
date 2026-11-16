//go:build edgeupgrade

package edgetest

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// startingPoints are the releases stable.json and beta.json name, and for
// a nightly build the one nightly.json names, as far as the release under
// test (EDGE_QUALIFY_BUILD and EDGE_QUALIFY_HOTFIX) is not below them:
// the upgrades hosts on each channel take. Without any it is the previous
// tag on HEAD's history, as before channels.
func startingPoints(t *testing.T, root, channel string) []start {
	channels := []string{selfupdate.ChannelStable, selfupdate.ChannelBeta}
	if channel == selfupdate.ChannelNightly {
		channels = append(channels, selfupdate.ChannelNightly)
	}
	var pointers []selfupdate.Pointer
	for _, ch := range channels {
		if p, ok := fetchPointer(t, ch); ok {
			pointers = append(pointers, p)
		}
	}
	var ours selfupdate.Mark
	for _, v := range []struct {
		name string
		n    *int
	}{{"EDGE_QUALIFY_BUILD", &ours.Build}, {"EDGE_QUALIFY_HOTFIX", &ours.Hotfix}} {
		if s := os.Getenv(v.name); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				t.Fatalf("%s=%q is not a non-negative integer", v.name, s)
			}
			*v.n = n
		}
	}
	starts := chooseStarts(pointers, ours)
	if len(starts) == 0 {
		starts = []start{{tag: strings.TrimSpace(mustOutput(t, "git", "-C", root, "describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", "HEAD^"))}}
	}
	t.Logf("starting points: %v", starts)
	return starts
}

// fetchPointer downloads and verifies channel's pointer from GitHub with
// curl, before this process reads any TLS roots. It reports false when
// GitHub answers 404: no pointer exists yet.
func fetchPointer(t *testing.T, channel string) (selfupdate.Pointer, bool) {
	t.Helper()
	url := selfupdate.PointerURL("https://github.com/"+selfupdate.Repo, channel)
	dir := t.TempDir()
	var got [2][]byte
	for i, u := range []string{url, url + ".sig"} {
		path := filepath.Join(dir, fmt.Sprint(i))
		code := strings.TrimSpace(mustOutput(t, "curl", "-sS", "-L", "--retry", "3", "-o", path, "-w", "%{http_code}", u))
		switch code {
		case "404":
			return selfupdate.Pointer{}, false
		case "200":
		default:
			t.Fatalf("GET %s: HTTP %s; the starting points come from the channel pointers", u, code)
		}
		var err error
		if got[i], err = os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
	}
	p, err := selfupdate.VerifyPointer(got[0], got[1], channel)
	if err != nil {
		t.Fatal(err)
	}
	return p, true
}

func mustOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return string(out)
}

// signedBuild builds context as this tree's test release n and signs its
// manifest, on stable at build qualifyBuild+n, upgrading from
// minUpgradeFrom or later. The fake GitHub serves it, and the docker stub
// runs this image's own release-check.
func (q *qualify) signedBuild(n int, context, minUpgradeFrom string) edgeImage {
	q.t.Helper()
	version := fmt.Sprintf("%s%d", qualifyVersion, n)
	img := q.build(version, context, qualifyBuild+n)
	if !img.updater {
		q.t.Fatalf("%s has no version --json", version)
	}
	var upgrade struct {
		Protocol selfupdate.EdgeProtocol `json:"protocol"`
	}
	data, err := os.ReadFile(filepath.Join(q.root, "packaging", "edge", "upgrade.json"))
	if err == nil {
		err = json.Unmarshal(data, &upgrade)
	}
	if err != nil {
		q.t.Fatal(err)
	}
	if !selfupdate.ValidTag(minUpgradeFrom) || selfupdate.Behind(version, minUpgradeFrom) {
		minUpgradeFrom = version
	}
	data, sig, err := selfupdate.SignManifest(selfupdate.Manifest{
		Schema: selfupdate.ManifestSchema, Channel: selfupdate.ChannelStable, Build: img.build,
		Version: version, Commit: strings.Repeat("0", 40), PublishedAt: time.Now().UTC(), StoreSchema: 1,
		Assets: map[string]selfupdate.Asset{"aether-edge-linux-amd64": {SHA256: strings.Repeat("0", 64), Size: 1}},
		Edge: &selfupdate.Edge{
			Image: "ghcr.io/3xdevops/aether-edge", ImageDigest: img.digest,
			StoreSchema: img.schema, StoreCompatFloor: img.floor, Protocol: upgrade.Protocol,
			MinUpgradeFrom: minUpgradeFrom,
		},
	}, qualifyKey)
	if err != nil {
		q.t.Fatal(err)
	}
	q.write(filepath.Join(q.releases, version+".json"), data, 0o644)
	q.write(filepath.Join(q.releases, version+".json.sig"), sig, 0o644)
	q.write(filepath.Join(q.signed, img.digest), []byte(version), 0o644)
	return img
}

// serveRelease answers GET /<repo>/releases/download/<rest> with a signed
// manifest signed wrote, or 404.
func (q *qualify) serveRelease(w http.ResponseWriter, rest string) {
	tag, name, _ := strings.Cut(rest, "/")
	q.mu.Lock()
	withdrawn := q.withdrawn[tag]
	if name == selfupdate.ManifestName {
		q.fetches[tag]++
	}
	q.mu.Unlock()
	file := ""
	switch name {
	case selfupdate.ManifestName:
		file = tag + ".json"
	case selfupdate.SignatureName:
		file = tag + ".json.sig"
	}
	data, err := os.ReadFile(filepath.Join(q.releases, file))
	if file == "" || withdrawn || err != nil {
		http.NotFound(w, nil)
		return
	}
	_, _ = w.Write(data)
}

func (q *qualify) withdraw(tag string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.withdrawn[tag] = true
}

func (q *qualify) fetched(tag string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.fetches[tag]
}

// requireFetched checks that the running release's own release-check
// asked the fake GitHub for tag's signed manifest.
func (q *qualify) requireFetched(tag string) {
	q.t.Helper()
	if q.fetched(tag) == 0 {
		q.t.Fatalf("release-check never downloaded the signed manifest of %s", tag)
	}
}

// requireKept checks that the updater kept each tag's signed manifest,
// verified by the test key, under its state directory.
func (q *qualify) requireKept(tags ...string) {
	q.t.Helper()
	for _, tag := range tags {
		path := filepath.Join(q.work, "state", "releases", tag+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			q.t.Fatal(err)
		}
		sig, err := os.ReadFile(path + ".sig")
		if err != nil {
			q.t.Fatal(err)
		}
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
		if !ed25519.Verify(qualifyKey.Public().(ed25519.PublicKey), data, raw) {
			q.t.Fatalf("the kept %s is not the manifest the fake GitHub signed", path)
		}
	}
}
