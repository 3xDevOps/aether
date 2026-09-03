package localops

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// The pinned version is the one thing in node.go that goes stale on its
// own. It has to stay a release on the 22-or-newer line, because that is
// what electron-builder needs.
func TestNodeVersionPin(t *testing.T) {
	semver := regexp.MustCompile(`^([0-9]+)\.[0-9]+\.[0-9]+$`)
	parts := semver.FindStringSubmatch(nodeVersion)
	if parts == nil {
		t.Fatalf("nodeVersion = %q, want a semver like 22.23.2", nodeVersion)
	}
	major, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if major < nodeMinMajor {
		t.Fatalf("nodeVersion = %q, want major %d or newer", nodeVersion, nodeMinMajor)
	}
}

func TestNodeReleaseFor(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		archive, bin string
		zipped       bool
	}{
		{"linux", "amd64", "node-v" + nodeVersion + "-linux-x64.tar.gz", "node-v" + nodeVersion + "-linux-x64/bin", false},
		{"linux", "arm64", "node-v" + nodeVersion + "-linux-arm64.tar.gz", "node-v" + nodeVersion + "-linux-arm64/bin", false},
		{"darwin", "amd64", "node-v" + nodeVersion + "-darwin-x64.tar.gz", "node-v" + nodeVersion + "-darwin-x64/bin", false},
		{"darwin", "arm64", "node-v" + nodeVersion + "-darwin-arm64.tar.gz", "node-v" + nodeVersion + "-darwin-arm64/bin", false},
		{"windows", "amd64", "node-v" + nodeVersion + "-win-x64.zip", "node-v" + nodeVersion + "-win-x64", true},
		{"windows", "arm64", "node-v" + nodeVersion + "-win-arm64.zip", "node-v" + nodeVersion + "-win-arm64", true},
	} {
		rel, err := nodeReleaseFor(tc.goos, tc.goarch)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.goos, tc.goarch, err)
		}
		if rel.archive != tc.archive || rel.bin != tc.bin || rel.zipped != tc.zipped {
			t.Errorf("%s/%s = %+v, want archive %q bin %q zipped %v", tc.goos, tc.goarch, rel, tc.archive, tc.bin, tc.zipped)
		}
	}
	for _, tc := range [][2]string{{"linux", "386"}, {"freebsd", "amd64"}, {"linux", "riscv64"}} {
		_, err := nodeReleaseFor(tc[0], tc[1])
		if err == nil {
			t.Fatalf("%s/%s: want an error", tc[0], tc[1])
		}
		if !strings.Contains(err.Error(), tc[0]+"/"+tc[1]) || !strings.Contains(err.Error(), "nodejs.org") {
			t.Errorf("%s/%s: error should name the platform and where to get Node: %v", tc[0], tc[1], err)
		}
	}
}

func TestEnsureNodeDownloadsThenReusesTheCache(t *testing.T) {
	requireScriptableNode(t)
	root := t.TempDir()
	hidePathNode(t)
	rel, dist := serveFakeNode(t, fakeNodeTree(), "")

	// An earlier pinned version and a download a Ctrl-C interrupted are
	// the two things that would otherwise sit in the cache forever.
	stale := filepath.Join(root, "v20.11.1")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(root, ".node-123.download")
	if err := os.WriteFile(leftover, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	tools, err := ensureNode(t.Context(), root, &out)
	if err != nil {
		t.Fatalf("ensureNode: %v", err)
	}
	for _, gone := range []string{stale, leftover} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived the bootstrap: %v", gone, err)
		}
	}
	binDir := filepath.Join(root, "v"+nodeVersion, filepath.FromSlash(rel.bin))
	if tools.pathDir != binDir {
		t.Errorf("pathDir = %q, want %q", tools.pathDir, binDir)
	}
	if tools.npm != filepath.Join(binDir, "npm") {
		t.Errorf("npm = %q, want it inside %q", tools.npm, binDir)
	}
	// npx ships as a symlink in the real tarball, so it has to survive
	// extraction as one.
	if info, err := os.Lstat(filepath.Join(binDir, "npx")); err != nil {
		t.Fatal(err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("npx = %v, want a symlink", info.Mode())
	}
	for _, want := range []string{"no node on PATH", dist.URL, "verified against SHASUMS256.txt", "extracted into " + filepath.Join(root, "v"+nodeVersion)} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("build output missing %q:\n%s", want, out.String())
		}
	}
	if got := dist.downloads.Load(); got != 1 {
		t.Fatalf("archive downloaded %d times, want 1", got)
	}

	out.Reset()
	if _, err := ensureNode(t.Context(), root, &out); err != nil {
		t.Fatalf("second ensureNode: %v", err)
	}
	if got := dist.downloads.Load(); got != 1 {
		t.Errorf("the cached copy was downloaded again (%d requests)", got)
	}
	if out.Len() != 0 {
		t.Errorf("a cache hit printed %q, want nothing", out.String())
	}
}

// The machine's own Node is what a developer already trusts, and using it
// is what keeps a build on a normal machine free of a 50 MB download.
func TestEnsureNodeUsesAGoodNodeOnPath(t *testing.T) {
	requireScriptableNode(t)
	stubs := stubNodeOnPath(t, "v24.11.0")
	_, dist := serveFakeNode(t, fakeNodeTree(), "")

	var out bytes.Buffer
	tools, err := ensureNode(t.Context(), t.TempDir(), &out)
	if err != nil {
		t.Fatalf("ensureNode: %v", err)
	}
	if tools.pathDir != "" {
		t.Errorf("pathDir = %q, want empty: PATH is not to be rewritten for a usable Node", tools.pathDir)
	}
	if tools.npm != filepath.Join(stubs, "npm") || tools.npx != filepath.Join(stubs, "npx") {
		t.Errorf("npm = %q, npx = %q, want the pair on PATH under %q", tools.npm, tools.npx, stubs)
	}
	if got := dist.downloads.Load(); got != 0 {
		t.Errorf("downloaded Node %d times with a usable one on PATH", got)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q, want nothing: this build has nothing to wait for", out.String())
	}
}

func TestEnsureNodeRefetchesABrokenCache(t *testing.T) {
	requireScriptableNode(t)
	root := t.TempDir()
	hidePathNode(t)
	rel, dist := serveFakeNode(t, fakeNodeTree(), "")

	// A cached tree that reports the wrong version is what an interrupted
	// download or a bumped nodeVersion leaves behind.
	binDir := filepath.Join(root, "v"+nodeVersion, filepath.FromSlash(rel.bin))
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"npm", "npx"} {
		if err := os.WriteFile(filepath.Join(binDir, tool), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(binDir, "node"), []byte("#!/bin/sh\necho v18.20.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "v"+nodeVersion, "stale.txt")
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureNode(t.Context(), root, &bytes.Buffer{}); err != nil {
		t.Fatalf("ensureNode: %v", err)
	}
	if got := dist.downloads.Load(); got != 1 {
		t.Errorf("archive downloaded %d times, want 1", got)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the broken cache was merged into, not replaced: %v", err)
	}
	if reported, err := nodeReports(t.Context(), filepath.Join(binDir, "node")); err != nil || reported != "v"+nodeVersion {
		t.Errorf("node reports %q (%v), want v%s", reported, err, nodeVersion)
	}
}

func TestEnsureNodeIgnoresAnOldNodeOnPath(t *testing.T) {
	requireScriptableNode(t)
	root := t.TempDir()
	// A node too old for electron-builder must not be used, and must not
	// stop the build either.
	stubNodeOnPath(t, "v18.20.0")
	rel, dist := serveFakeNode(t, fakeNodeTree(), "")

	var out bytes.Buffer
	tools, err := ensureNode(t.Context(), root, &out)
	if err != nil {
		t.Fatalf("ensureNode: %v", err)
	}
	if want := filepath.Join(root, "v"+nodeVersion, filepath.FromSlash(rel.bin)); tools.pathDir != want {
		t.Fatalf("pathDir = %q, want the downloaded copy at %q", tools.pathDir, want)
	}
	if got := dist.downloads.Load(); got != 1 {
		t.Errorf("archive downloaded %d times, want 1", got)
	}
	if !strings.Contains(out.String(), "older than v22") {
		t.Errorf("build output should say why the machine's node was skipped:\n%s", out.String())
	}
}

func TestEnsureNodeRejectsAChecksumMismatch(t *testing.T) {
	requireScriptableNode(t)
	root := t.TempDir()
	hidePathNode(t)
	// A digest for some other archive is what a tampered mirror or a
	// truncated transfer looks like.
	rel, _ := serveFakeNode(t, fakeNodeTree(), strings.Repeat("a", 64))

	_, err := ensureNode(t.Context(), root, &bytes.Buffer{})
	if err == nil {
		t.Fatal("ensureNode accepted an archive that failed its checksum")
	}
	for _, want := range []string{"checksum", rel.archive} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q: %v", want, err)
		}
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		t.Errorf("the failed download left %s behind", e.Name())
	}
}

func TestEnsureNodeReportsAnUnreachableServer(t *testing.T) {
	requireScriptableNode(t)
	hidePathNode(t)
	_, dist := serveFakeNode(t, fakeNodeTree(), "")
	dist.Close() // nothing is listening now, the way an offline machine looks

	_, err := ensureNode(t.Context(), t.TempDir(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("ensureNode succeeded with no server")
	}
	// The user has to be able to fetch the file by hand from the message.
	if !strings.Contains(err.Error(), dist.URL+"/v"+nodeVersion+"/") {
		t.Errorf("error should carry the URL to fetch: %v", err)
	}
}

func TestExtractNodeTarGz(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	archive := writeArchive(t, tarGz(t, []archiveFile{
		{name: "node-v1/bin", dir: true},
		{name: "node-v1/bin/node", body: "binary", mode: 0o755},
		{name: "node-v1/bin/npx", link: "node"},
		{name: "node-v1/README.md", body: "readme"},
	}), "node.tar.gz")

	if err := extractNode(archive, dir, false); err != nil {
		t.Fatalf("extractNode: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "node-v1", "bin", "node"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("node lost its execute bit: %v", info.Mode())
	}
	if target, err := os.Readlink(filepath.Join(dir, "node-v1", "bin", "npx")); err != nil || target != "node" {
		t.Errorf("npx link = %q (%v), want \"node\"", target, err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "node-v1", "README.md")); err != nil || string(body) != "readme" {
		t.Errorf("README.md = %q (%v)", body, err)
	}
}

func TestExtractNodeZip(t *testing.T) {
	dir := t.TempDir()
	archive := writeArchive(t, zipped(t, []archiveFile{
		{name: "node-v1/", dir: true},
		{name: "node-v1/node.exe", body: "binary"},
		{name: "node-v1/npm.cmd", body: "@echo off"},
	}), "node.zip")

	if err := extractNode(archive, dir, true); err != nil {
		t.Fatalf("extractNode: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "node-v1", "npm.cmd")); err != nil || string(body) != "@echo off" {
		t.Errorf("npm.cmd = %q (%v)", body, err)
	}
}

// A dist server that served a matching SHASUMS256.txt would still not get
// to write outside the cache directory.
func TestExtractNodeRejectsPathTraversal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
	}{
		{"parent", "node-v1/../../escaped"},
		{"bare parent", "../escaped"},
		{"absolute", "/tmp/escaped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, format := range []string{"tar.gz", "zip"} {
				dir := t.TempDir()
				var archive string
				if format == "zip" {
					archive = writeArchive(t, zipped(t, []archiveFile{{name: tc.entry, body: "x"}}), "node.zip")
				} else {
					archive = writeArchive(t, tarGz(t, []archiveFile{{name: tc.entry, body: "x"}}), "node.tar.gz")
				}
				err := extractNode(archive, dir, format == "zip")
				if err == nil {
					t.Fatalf("%s: extractNode accepted %q", format, tc.entry)
				}
				if !strings.Contains(err.Error(), "outside") {
					t.Errorf("%s: error should say the entry escapes: %v", format, err)
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped")); !os.IsNotExist(err) {
					t.Fatalf("%s: the entry was written outside the tree: %v", format, err)
				}
			}
		})
	}
}

// --- helpers -----------------------------------------------------------

// requireScriptableNode skips the tests whose fake Node has to run. Only a
// real node.exe would do on windows, and the real one is what these tests
// exist to avoid downloading.
func requireScriptableNode(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake node is a shell script, which windows cannot execute")
	}
}

func requireSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege windows does not grant by default")
	}
}

// stubNodeOnPath puts a node reporting version, with npm and npx beside
// it, on PATH as the only entry, and returns the directory holding them.
func stubNodeOnPath(t *testing.T, version string) string {
	t.Helper()
	stubs := t.TempDir()
	for tool, body := range map[string]string{
		"node": "#!/bin/sh\necho " + version + "\n",
		"npm":  "#!/bin/sh\n",
		"npx":  "#!/bin/sh\n",
	} {
		if err := os.WriteFile(filepath.Join(stubs, tool), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", stubs)
	return stubs
}

// hidePathNode empties PATH so the machine's own Node cannot answer.
func hidePathNode(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// fakeNodeTree is the archive layout of a real release, small enough to
// serve from memory: node reports the pinned version, npm sits beside it
// and npx is the symlink nodejs.org ships.
func fakeNodeTree() []archiveFile {
	return []archiveFile{
		{name: "bin", dir: true},
		{name: "bin/node", body: "#!/bin/sh\necho v" + nodeVersion + "\n", mode: 0o755},
		{name: "bin/npm", body: "#!/bin/sh\n", mode: 0o755},
		{name: "bin/npx", link: "node"},
		{name: "README.md", body: "fake"},
	}
}

// serveFakeNode packs files into the archive this platform would download,
// serves it with its SHASUMS256.txt, and points nodeDistBase at the server
// for the rest of the test. A digest other than "" is what the checksum
// file advertises, for the mismatch case.
func serveFakeNode(t *testing.T, files []archiveFile, digest string) (nodeRelease, *fakeDist) {
	t.Helper()
	rel, err := nodeReleaseFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("no pinned Node for this platform: %v", err)
	}
	top := strings.SplitN(rel.bin, "/", 2)[0]
	packed := make([]archiveFile, 0, len(files))
	for _, f := range files {
		f.name = top + "/" + f.name
		packed = append(packed, f)
	}
	var archive []byte
	if rel.zipped {
		archive = zipped(t, packed)
	} else {
		archive = tarGz(t, packed)
	}
	if digest == "" {
		sum := sha256.Sum256(archive)
		digest = hex.EncodeToString(sum[:])
	}

	dist := &fakeDist{}
	mux := http.NewServeMux()
	base := "/v" + nodeVersion + "/"
	mux.HandleFunc(base+"SHASUMS256.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  node-v%s.tar.xz\n%s  %s\n", strings.Repeat("0", 64), nodeVersion, digest, rel.archive)
	})
	mux.HandleFunc(base+rel.archive, func(w http.ResponseWriter, _ *http.Request) {
		dist.downloads.Add(1)
		_, _ = w.Write(archive)
	})
	dist.Server = httptest.NewServer(mux)
	old := nodeDistBase
	nodeDistBase = dist.URL
	t.Cleanup(func() {
		dist.Close()
		nodeDistBase = old
	})
	return rel, dist
}

// fakeDist is a nodejs.org release directory: one archive and the
// SHASUMS256.txt listing it. downloads counts archive requests, which is
// how a cache hit is told from a fetch.
type fakeDist struct {
	*httptest.Server
	downloads atomic.Int64
}

// archiveFile is one entry to pack: a directory, a regular file, or a
// symlink when link is set.
type archiveFile struct {
	name string
	body string
	link string
	mode int64
	dir  bool
}

func tarGz(t *testing.T, files []archiveFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		hdr := &tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		switch {
		case f.dir:
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeDir, 0o755, 0
		case f.link != "":
			hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeSymlink, f.link, 0
		case f.mode == 0:
			hdr.Mode = 0o644
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(f.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipped(t *testing.T, files []archiveFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		if f.dir || f.link != "" {
			// The windows zips carry no symlinks; a directory entry is
			// implied by the paths inside it.
			continue
		}
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeArchive(t *testing.T, body []byte, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
