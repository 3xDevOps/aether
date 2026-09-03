// Package selfupdate downloads released Aether binaries and swaps them in
// place. It mirrors what scripts/install.sh does - resolve the latest tag
// via the GitHub releases redirect, download the platform asset, verify it
// against checksums.txt - so the two must stay in step with the release
// asset names.
package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Repo is the GitHub repository releases are published from.
const Repo = "3xDevOps/Aether"

// maxAssetSize bounds a downloaded asset; release binaries are tens of
// megabytes, so a gigabyte means something is very wrong upstream.
const maxAssetSize = 1 << 30

// httpClient downloads checksums and assets. Default redirect handling is
// wanted here: GitHub release asset URLs redirect to a CDN.
var httpClient = &http.Client{Timeout: 5 * time.Minute}

// LatestTag resolves the newest release tag from a GitHub /releases/latest
// URL, which redirects to /releases/tag/<tag>. No API token, no rate limit.
func LatestTag(ctx context.Context, latestURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return "", err
	}
	var redirect string
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			redirect = req.URL.Path
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve latest release: %w", err)
	}
	_ = resp.Body.Close()
	const marker = "/releases/tag/"
	i := strings.LastIndex(redirect, marker)
	if i < 0 {
		return "", errors.New("no published release found; pass --version <tag>")
	}
	tag := redirect[i+len(marker):]
	if tag == "" {
		return "", errors.New("no published release found; pass --version <tag>")
	}
	return tag, nil
}

// Apply downloads baseURL/<asset>, verifies it against baseURL/checksums.txt,
// and atomically replaces dst with it. The destination keeps mode 0755; a
// failure at any point leaves dst untouched.
func Apply(ctx context.Context, baseURL, asset, dst string) error {
	sums, err := fetch(ctx, baseURL+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}
	want, err := ChecksumFor(sums, asset)
	if err != nil {
		return err
	}

	// Stage in dst's directory so the final rename is atomic (same
	// filesystem) and never leaves a half-written binary on PATH.
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".update-*")
	if err != nil {
		return fmt.Errorf("stage update in %s: %w", dir, err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	body, err := fetch(ctx, baseURL+"/"+asset)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset, want, got)
	}
	if _, err := tmp.Write(body); err != nil {
		return fmt.Errorf("write staged binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return fmt.Errorf("chmod staged binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close staged binary: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("replace %s: %w", dst, err)
	}
	return nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetSize))
	if err != nil {
		return nil, err
	}
	return body, nil
}

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
