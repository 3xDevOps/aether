package localops

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// nodeVersion is the Node.js release `aether gui build` downloads when this
// machine has no Node of its own. Bump it with the Node 22 LTS line;
// https://nodejs.org/dist/index.json lists every published release.
const nodeVersion = "22.23.2"

// nodeMinMajor is the oldest Node the desktop build accepts from PATH.
// electron-builder 26, the version desktop/package.json pins, needs 22 or
// newer.
const nodeMinMajor = 22

// nodeDistBase is the directory nodejs.org publishes releases under. It is
// a variable so the tests can serve their own archive; no flag exposes it,
// because a build must never be pointed at an unverified mirror.
var nodeDistBase = "https://nodejs.org/dist"

// nodeHTTP downloads the release. The timeout covers the whole transfer of
// an archive around 50 MB, so it is generous rather than tight.
var nodeHTTP = &http.Client{Timeout: 15 * time.Minute}

// maxNodeArchive and maxNodeTree bound what a dist server can make this
// machine write. The archive is verified before it is extracted, but a
// mirror that serves a matching SHASUMS256.txt controls both, so the
// extracted tree is bounded too.
const (
	maxNodeArchive = 512 << 20
	maxNodeTree    = 1 << 30
)

// nodeTools are the executables the desktop build runs. pathDir is the
// directory to prepend to PATH so npm and npx find their own node; it is
// empty when the tools came from PATH already.
type nodeTools struct {
	npm     string
	npx     string
	pathDir string
}

// nodeRelease is where the pinned Node lives for one platform: the archive
// nodejs.org publishes for it, and the directory inside that archive
// holding node, npm and npx. The unix tarballs keep them in bin/; the
// Windows zips keep them in the archive root.
type nodeRelease struct {
	archive string
	bin     string
	zipped  bool
}

// nodeCacheDir is where the private Node copies live, beside the desktop
// build directory. Deleting it costs one download, nothing else.
func nodeCacheDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("localops: cache dir: %w", err)
	}
	return filepath.Join(cache, "aether", "node"), nil
}

// ensureNode returns the npm and npx the desktop build should run. A node
// of nodeMinMajor or newer on PATH, with npm and npx beside it, is used
// as it stands. Otherwise the pinned release is downloaded into root and
// used for this build only: nothing outside root changes and no shell
// profile is touched. Progress goes to out, because the first download
// makes a build that is otherwise quick take minutes.
func ensureNode(ctx context.Context, root string, out io.Writer) (nodeTools, error) {
	tools, reason := systemNode(ctx)
	if reason == "" {
		return tools, nil
	}
	return bootstrapNode(ctx, root, reason, out)
}

// systemNode reports the tools already on PATH. The empty reason means
// they are usable; any other reason is why the caller must bootstrap, and
// it is printed, so it names the machine's own state.
func systemNode(ctx context.Context) (nodeTools, string) {
	node, err := exec.LookPath("node")
	if err != nil {
		return nodeTools{}, "no node on PATH"
	}
	reported, err := nodeReports(ctx, node)
	if err != nil {
		return nodeTools{}, fmt.Sprintf("%s does not run (%v)", node, err)
	}
	major, err := nodeMajor(reported)
	if err != nil {
		return nodeTools{}, fmt.Sprintf("%s printed %q, not a version", node, reported)
	}
	if major < nodeMinMajor {
		return nodeTools{}, fmt.Sprintf("%s is %s, older than v%d", node, reported, nodeMinMajor)
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return nodeTools{}, "no npm on PATH"
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		return nodeTools{}, "no npx on PATH"
	}
	return nodeTools{npm: npm, npx: npx}, ""
}

// nodeReports runs `node --version` and returns what it printed, trimmed.
func nodeReports(ctx context.Context, node string) (string, error) {
	out, err := exec.CommandContext(ctx, node, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// nodeMajor parses the "v22.23.2" that `node --version` prints.
func nodeMajor(reported string) (int, error) {
	digits := strings.TrimPrefix(reported, "v")
	if i := strings.IndexByte(digits, '.'); i >= 0 {
		digits = digits[:i]
	}
	major, err := strconv.Atoi(digits)
	if err != nil {
		return 0, fmt.Errorf("localops: parse node version %q: %w", reported, err)
	}
	return major, nil
}

// nodeReleaseFor names the archive for one platform. Only the platforms
// the desktop app itself targets are listed; anything else is an error
// naming what the user has to install by hand, never a silent fallback to
// whatever node the machine happens to carry.
func nodeReleaseFor(goos, goarch string) (nodeRelease, error) {
	var arch string
	switch goarch {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	}
	var platform string
	switch {
	case arch == "":
	case goos == "linux", goos == "darwin":
		platform = goos + "-" + arch
	case goos == "windows":
		platform = "win-" + arch
	}
	if platform == "" {
		return nodeRelease{}, fmt.Errorf("localops: nodejs.org publishes no Node.js %s build for %s/%s; install Node.js %d+ yourself (https://nodejs.org) and run this again", nodeVersion, goos, goarch, nodeMinMajor)
	}
	stem := "node-v" + nodeVersion + "-" + platform
	if goos == "windows" {
		return nodeRelease{archive: stem + ".zip", bin: stem, zipped: true}, nil
	}
	return nodeRelease{archive: stem + ".tar.gz", bin: stem + "/bin"}, nil
}

// bootstrapNode returns the private Node copy under root, downloading it
// unless a cached copy already answers with the pinned version. A cached
// copy that does not answer is deleted rather than trusted: a half
// extracted tree fails npm in ways that read like a bug in Aether.
func bootstrapNode(ctx context.Context, root, reason string, out io.Writer) (nodeTools, error) {
	rel, err := nodeReleaseFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nodeTools{}, err
	}
	versionDir := filepath.Join(root, "v"+nodeVersion)
	binDir := filepath.Join(versionDir, filepath.FromSlash(rel.bin))
	tools, err := nodeToolsIn(ctx, binDir)
	if err == nil {
		return tools, nil
	}
	if err = os.RemoveAll(versionDir); err != nil {
		return nodeTools{}, fmt.Errorf("localops: clear %s: %w", versionDir, err)
	}
	if err = os.MkdirAll(root, 0o755); err != nil {
		return nodeTools{}, fmt.Errorf("localops: create %s: %w", root, err)
	}

	url := nodeDistBase + "/v" + nodeVersion + "/" + rel.archive
	_, _ = fmt.Fprintf(out, "Node.js: %s. Downloading a private copy for this build only.\n", reason)
	archive, err := fetchNodeArchive(ctx, root, rel, url, out)
	if archive != "" {
		defer func() { _ = os.Remove(archive) }()
	}
	if err != nil {
		return nodeTools{}, err
	}
	_, _ = fmt.Fprintln(out, "  verified against SHASUMS256.txt")

	// Extract beside the final directory and rename, so an interrupted
	// build never leaves a tree that the next one would reuse.
	staging := filepath.Join(root, ".v"+nodeVersion+".part")
	if err = os.RemoveAll(staging); err != nil {
		return nodeTools{}, fmt.Errorf("localops: clear %s: %w", staging, err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err = os.MkdirAll(staging, 0o755); err != nil {
		return nodeTools{}, fmt.Errorf("localops: create %s: %w", staging, err)
	}
	if err = extractNode(archive, staging, rel.zipped); err != nil {
		return nodeTools{}, fmt.Errorf("localops: extract %s: %w", url, err)
	}
	if err = os.Rename(staging, versionDir); err != nil {
		return nodeTools{}, fmt.Errorf("localops: move the extracted Node.js into %s: %w", versionDir, err)
	}
	_, _ = fmt.Fprintf(out, "  extracted into %s\n", versionDir)

	if tools, err = nodeToolsIn(ctx, binDir); err != nil {
		return nodeTools{}, fmt.Errorf("localops: the Node.js downloaded from %s does not run: %w", url, err)
	}
	return tools, nil
}

// nodeToolsIn reports the tools in binDir, and fails unless node there
// runs and reports exactly the pinned version. That check is what makes a
// cached copy safe to reuse and a broken one safe to delete.
func nodeToolsIn(ctx context.Context, binDir string) (nodeTools, error) {
	node, npm, npx := filepath.Join(binDir, "node"), filepath.Join(binDir, "npm"), filepath.Join(binDir, "npx")
	if runtime.GOOS == "windows" {
		node, npm, npx = node+".exe", npm+".cmd", npx+".cmd"
	}
	for _, tool := range []string{npm, npx} {
		if _, err := os.Stat(tool); err != nil {
			return nodeTools{}, fmt.Errorf("localops: %s: %w", tool, err)
		}
	}
	reported, err := nodeReports(ctx, node)
	if err != nil {
		return nodeTools{}, fmt.Errorf("localops: %s --version: %w", node, err)
	}
	if reported != "v"+nodeVersion {
		return nodeTools{}, fmt.Errorf("localops: %s reports %s, not v%s", node, reported, nodeVersion)
	}
	return nodeTools{npm: npm, npx: npx, pathDir: binDir}, nil
}

// fetchNodeArchive downloads url into dir and returns the file it wrote,
// which is deleted unless its SHA-256 matches the digest SHASUMS256.txt
// lists for the archive. The path is returned even on failure so the
// caller can clean up a partial download.
func fetchNodeArchive(ctx context.Context, dir string, rel nodeRelease, url string, out io.Writer) (string, error) {
	sumsURL := nodeDistBase + "/v" + nodeVersion + "/SHASUMS256.txt"
	sums, err := getNode(ctx, sumsURL)
	if err != nil {
		return "", fmt.Errorf("localops: download %s: %w", sumsURL, err)
	}
	defer func() { _ = sums.Body.Close() }()
	digests, err := io.ReadAll(io.LimitReader(sums.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("localops: read %s: %w", sumsURL, err)
	}
	want, err := selfupdate.ChecksumFor(digests, rel.archive)
	if err != nil {
		return "", fmt.Errorf("localops: %s: %w", sumsURL, err)
	}

	resp, err := getNode(ctx, url)
	if err != nil {
		return "", fmt.Errorf("localops: download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = fmt.Fprintf(out, "  %s%s\n", url, megabytes(resp.ContentLength))
	f, err := os.CreateTemp(dir, ".node-*.download")
	if err != nil {
		return "", fmt.Errorf("localops: stage the Node.js download in %s: %w", dir, err)
	}
	name := f.Name()
	sum := sha256.New()
	written, err := io.Copy(f, io.TeeReader(io.LimitReader(resp.Body, maxNodeArchive), sum))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return name, fmt.Errorf("localops: download %s: %w", url, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		return name, fmt.Errorf("localops: %s failed its checksum: SHASUMS256.txt lists %s, the %d bytes downloaded hash to %s", url, want, written, got)
	}
	return name, nil
}

// getNode issues the GET and returns the response for the caller to close.
func getNode(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := nodeHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, errors.New(resp.Status)
	}
	return resp, nil
}

// megabytes describes a download size for the progress line, and says
// nothing when the server sent no Content-Length.
func megabytes(n int64) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d MB)", (n+512<<10)/(1<<20))
}

// extractNode unpacks the release archive into dir. Every write goes
// through an os.Root confined to dir, so an entry naming ../ or reached
// through a symlink fails instead of writing outside the cache.
func extractNode(archive, dir string, zipped bool) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if zipped {
		return extractNodeZip(archive, root)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return extractNodeTarGz(f, root)
}

func extractNodeTarGz(r io.Reader, root *os.Root) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	budget := int64(maxNodeTree)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name, err := archiveEntry(hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(name, 0o755)
		case tar.TypeReg:
			err = writeArchiveFile(root, name, tr, fs.FileMode(hdr.Mode).Perm(), &budget)
		case tar.TypeSymlink:
			if err = parentDir(root, name); err == nil {
				err = root.Symlink(hdr.Linkname, name)
			}
		default:
			// Node's tarballs carry directories, files and symlinks only.
			// Anything else means this is not the archive we asked for.
			err = fmt.Errorf("unexpected entry type %q for %s", hdr.Typeflag, hdr.Name)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", hdr.Name, err)
		}
	}
}

func extractNodeZip(archive string, root *os.Root) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	budget := int64(maxNodeTree)
	for _, entry := range zr.File {
		name, err := archiveEntry(entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err = root.MkdirAll(name, 0o755); err != nil {
				return fmt.Errorf("%s: %w", entry.Name, err)
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			// The Windows zips are files and directories only.
			return fmt.Errorf("%s: unexpected entry mode %s", entry.Name, entry.Mode())
		}
		src, err := entry.Open()
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Name, err)
		}
		err = writeArchiveFile(root, name, src, entry.Mode().Perm(), &budget)
		_ = src.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Name, err)
		}
	}
	return nil
}

// writeArchiveFile copies one entry into the confined tree, drawing its
// size from the shared budget so a compressed archive cannot expand into
// an unbounded write.
func writeArchiveFile(root *os.Root, name string, src io.Reader, mode fs.FileMode, budget *int64) error {
	if err := parentDir(root, name); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	written, err := io.Copy(f, io.LimitReader(src, *budget+1))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if written > *budget {
		return fmt.Errorf("the archive expands past %d bytes", maxNodeTree)
	}
	*budget -= written
	return nil
}

// parentDir creates the directories an entry needs. Archives are not
// required to list a directory before the files inside it.
func parentDir(root *os.Root, name string) error {
	if dir := path.Dir(name); dir != "." {
		return root.MkdirAll(dir, 0o755)
	}
	return nil
}

// archiveEntry cleans an archive entry name and rejects the ones that
// would land outside the tree. os.Root refuses these as well; checking
// here is what turns a kernel-level "path escapes from parent" into a
// message naming the archive that carried the entry.
func archiveEntry(name string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(name, `\`, "/"))
	if clean == "." {
		return "", errors.New("empty entry name")
	}
	if path.IsAbs(clean) || filepath.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("entry %q would extract outside the cache directory", name)
	}
	return clean, nil
}
