// Package selfupdate downloads released Aether binaries and swaps them in
// place. A release is installable only once its signed manifest,
// aether-release.json, is published: every download is checked against the
// sha256 and size it lists. scripts/install.sh still verifies against
// checksums.txt, so the asset names must stay in step with it.
package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Repo is the GitHub repository releases are published from.
const Repo = "3xDevOps/Aether"

// httpClient downloads manifests and assets. GitHub release asset URLs
// redirect to a CDN, so redirects are followed, but never off https. The
// timeout covers a whole transfer, the largest being the desktop app zip of
// around 100 MB.
var httpClient = &http.Client{Timeout: 15 * time.Minute, CheckRedirect: HTTPSOnly}

// LatestTag resolves the newest release tag from a GitHub /releases/latest
// URL, which redirects - possibly through more than one hop - to
// /releases/tag/<tag>. No API token, no rate limit. A repository without a
// release redirects to its release list instead, which is ErrNoRelease;
// any other non-200 answer is returned as the error it is.
func LatestTag(ctx context.Context, latestURL string) (string, error) {
	var tag string
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := HTTPSOnly(req, via); err != nil {
				return err
			}
			if t, ok := tagIn(req.URL); ok {
				tag = t
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	err := retry(ctx, func() (bool, error) {
		tag = ""
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return ctx.Err() == nil && !errors.Is(err, errInsecureRedirect), fmt.Errorf("resolve latest release: %w", err)
		}
		_ = resp.Body.Close()
		if tag != "" {
			return false, nil
		}
		code := resp.StatusCode
		if t, ok := tagIn(resp.Request.URL); ok && code == http.StatusOK {
			tag = t
			return false, nil
		}
		if code == http.StatusOK {
			return false, fmt.Errorf("%w: %s led to %s, not a release tag; pass --version <tag>", ErrNoRelease, latestURL, resp.Request.URL.Redacted())
		}
		return code >= 500 || code == http.StatusTooManyRequests, fmt.Errorf("resolve latest release: GET %s: %s", latestURL, resp.Status)
	})
	if err != nil {
		return "", err
	}
	if !ValidTag(tag) {
		return "", fmt.Errorf("the latest release tag %q is not a release tag", tag)
	}
	return tag, nil
}

// tagIn reads the tag out of a /releases/tag/<tag> URL.
func tagIn(u *url.URL) (string, bool) {
	const marker = "/releases/tag/"
	i := strings.LastIndex(u.Path, marker)
	if i < 0 || u.Path[i+len(marker):] == "" {
		return "", false
	}
	return u.Path[i+len(marker):], true
}

// Staged is one verified release asset written beside its destination and
// ready to be renamed over it. Splitting the download from the rename is
// what lets a caller replacing several binaries verify all of them before
// it replaces any: see UpdateBinaries.
type Staged struct {
	// dst is the binary this replaces, tmp the verified file staged for it.
	dst, tmp string
	// sum is the hex SHA-256 the manifest lists for the asset, which the
	// staged file was verified against. An authorized install checks the
	// copy root makes against it a second time.
	sum string
	// committed stops Discard from removing a file that is now the
	// destination.
	committed bool
}

// Stage downloads the asset m lists, verifies its size and sha256 against
// m, and writes it next to dst with mode 0755. It changes nothing the
// running system can see; Commit does that. The caller must call Discard
// (Commit or not) so a staged file never outlives the attempt.
func Stage(ctx context.Context, m Manifest, asset, dst string) (*Staged, error) {
	// Beside dst, so the final rename is atomic (same filesystem) and
	// never leaves a half-written binary on PATH.
	return stageIn(ctx, m, asset, dst, filepath.Dir(dst))
}

// stageIn is Stage with the staging directory chosen by the caller: dst's
// own directory when it is writable, a private directory of the user's
// when root has to make the copy.
func stageIn(ctx context.Context, m Manifest, asset, dst, dir string) (*Staged, error) {
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".update-*")
	if err != nil {
		return nil, fmt.Errorf("stage update in %s: %w", dir, err)
	}
	staged := &Staged{dst: dst, tmp: tmp.Name()}
	want, err := m.Download(ctx, asset, tmp)
	if err != nil {
		staged.close(tmp)
		return nil, err
	}
	staged.sum = want.SHA256
	if err := tmp.Chmod(0o755); err != nil {
		staged.close(tmp)
		return nil, fmt.Errorf("chmod staged binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		staged.close(tmp)
		return nil, fmt.Errorf("sync staged binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		staged.Discard()
		return nil, fmt.Errorf("close staged binary: %w", err)
	}
	return staged, nil
}

// close abandons a staged file whose handle is still open.
func (s *Staged) close(f *os.File) {
	_ = f.Close()
	s.Discard()
}

// Commit renames the staged file over its destination and syncs the
// directory so a crash cannot undo the rename. Windows cannot sync a
// directory handle.
func (s *Staged) Commit() error {
	if err := os.Rename(s.tmp, s.dst); err != nil {
		return fmt.Errorf("replace %s: %w", s.dst, err)
	}
	s.committed = true
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(s.dst))
	if err == nil {
		err = dir.Sync()
		if cerr := dir.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return fmt.Errorf("replaced %s but did not sync its directory: %w", s.dst, err)
	}
	return nil
}

// Discard removes the staged file. It is a no-op after Commit and safe to
// call more than once.
func (s *Staged) Discard() {
	if s.committed || s.tmp == "" {
		return
	}
	_ = os.Remove(s.tmp)
	s.tmp = ""
}

// Path is the destination this staged asset replaces.
func (s *Staged) Path() string { return s.dst }

// ChecksumFor finds asset's digest in sha256sum-format output, accepting the
// binary-mode "*name" marker. This project's checksums.txt and nodejs.org's
// SHASUMS256.txt are both in that format, so localops verifies its Node.js
// download with this too.
func ChecksumFor(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == asset {
			return fields[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("parse checksums.txt: %w", err)
	}
	return "", fmt.Errorf("%s is not listed in checksums.txt", asset)
}
