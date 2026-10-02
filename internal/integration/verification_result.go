package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	verificationResultMaxErrorBytes    = 8 << 10
	verificationResultMaxArtifactBytes = (protocol.IntegrationMaxOutputBytes+verificationResultMaxErrorBytes)*6 + 64<<10
)

type verificationResultArtifact struct {
	Version           int                         `json:"version"`
	WorkspaceID       string                      `json:"workspace_id"`
	CandidateID       string                      `json:"candidate_id"`
	VerificationID    string                      `json:"verification_id"`
	CandidateRevision string                      `json:"candidate_revision"`
	CreationKey       string                      `json:"creation_key"`
	CreatedAt         time.Time                   `json:"created_at"`
	FinishedAt        *time.Time                  `json:"finished_at"`
	ExpiresAt         time.Time                   `json:"expires_at"`
	Status            protocol.VerificationStatus `json:"status"`
	ExitCode          *int                        `json:"exit_code"`
	Output            string                      `json:"output"`
	OutputTruncated   bool                        `json:"output_truncated"`
	Error             string                      `json:"error"`
}

func normalizeVerificationOutput(output []byte, truncated bool) (string, bool) {
	text := strings.ToValidUTF8(string(output), "\uFFFD")
	if len(text) <= protocol.IntegrationMaxOutputBytes {
		return text, truncated
	}
	return truncateVerificationUTF8(text, protocol.IntegrationMaxOutputBytes), true
}

func normalizeVerificationError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToValidUTF8(err.Error(), "\uFFFD")
	if len(text) <= verificationResultMaxErrorBytes {
		return text
	}
	return truncateVerificationUTF8(text, verificationResultMaxErrorBytes)
}

func truncateVerificationUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && limit < len(text) && (text[limit]&0xc0) == 0x80 {
		limit--
	}
	return text[:limit]
}

func verificationResultPath(root, candidateID, verificationID string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: artifact root unavailable", ErrUnavailable)
	}
	if !cleanID(candidateID) || candidateID == "." || candidateID == ".." ||
		!cleanID(verificationID) || verificationID == "." || verificationID == ".." {
		return "", fmt.Errorf("%w: invalid verification artifact identity", ErrInvalidRequest)
	}
	return filepath.Join(root, "candidates", candidateID, "verifications", verificationID+".result"), nil
}

func writeVerificationResult(path string, result verificationResultArtifact) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(body) > verificationResultMaxArtifactBytes {
		return fmt.Errorf("%w: verification result artifact exceeds bound", ErrInvalidRequest)
	}
	dir := filepath.Dir(path)
	if mkdirErr := ensureDir(dir); mkdirErr != nil {
		return mkdirErr
	}
	tmp, err := os.CreateTemp(dir, ".verification-result-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if chmodErr := tmp.Chmod(0o600); chmodErr != nil {
		_ = tmp.Close()
		return chmodErr
	}
	if n, writeErr := tmp.Write(body); writeErr != nil {
		_ = tmp.Close()
		return writeErr
	} else if n != len(body) {
		_ = tmp.Close()
		return io.ErrShortWrite
	}
	if syncErr := tmp.Sync(); syncErr != nil {
		_ = tmp.Close()
		return syncErr
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return closeErr
	}
	if renameErr := os.Rename(tmpPath, path); renameErr != nil {
		return renameErr
	}
	removeTemp = false
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	if syncErr := dirFile.Sync(); syncErr != nil {
		_ = dirFile.Close()
		return syncErr
	}
	return dirFile.Close()
}

func readVerificationResult(path string) (verificationResultArtifact, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return verificationResultArtifact{}, false, nil
	}
	if err != nil {
		return verificationResultArtifact{}, false, err
	}
	if !info.Mode().IsRegular() {
		return verificationResultArtifact{}, false, fmt.Errorf("%w: verification result is not a regular file", ErrUnavailable)
	}
	if info.Size() > verificationResultMaxArtifactBytes {
		return verificationResultArtifact{}, false, fmt.Errorf("%w: verification result artifact exceeds bound", ErrUnavailable)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return verificationResultArtifact{}, false, err
	}
	if len(body) > verificationResultMaxArtifactBytes {
		return verificationResultArtifact{}, false, fmt.Errorf("%w: verification result artifact exceeds bound", ErrUnavailable)
	}
	if !utf8.Valid(body) {
		return verificationResultArtifact{}, false, fmt.Errorf("%w: verification result artifact is not valid UTF-8", ErrUnavailable)
	}
	var result verificationResultArtifact
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&result); decodeErr != nil {
		return verificationResultArtifact{}, false, fmt.Errorf("%w: decode verification result: %v", ErrUnavailable, decodeErr)
	}
	var extra any
	if decodeErr := decoder.Decode(&extra); !errors.Is(decodeErr, io.EOF) {
		if decodeErr == nil {
			return verificationResultArtifact{}, false, fmt.Errorf("%w: trailing verification result data", ErrUnavailable)
		}
		return verificationResultArtifact{}, false, fmt.Errorf("%w: decode trailing verification result: %v", ErrUnavailable, decodeErr)
	}
	return result, true, nil
}

func cloneVerificationExitCode(exit *int) *int {
	if exit == nil {
		return nil
	}
	value := *exit
	return &value
}

func validateVerificationResult(result verificationResultArtifact, c *protocol.Candidate, v protocol.Verification) error {
	if c == nil {
		return fmt.Errorf("%w: missing candidate for verification result", ErrUnavailable)
	}
	if result.Version != 1 || result.WorkspaceID != c.WorkspaceID || result.CandidateID != c.CandidateID ||
		result.VerificationID != v.VerificationID || result.CandidateRevision != c.CandidateRevision ||
		result.CandidateRevision != v.CandidateRevision || result.CreationKey == "" ||
		result.CreationKey != v.CreationKey || !result.CreatedAt.Equal(v.CreatedAt) ||
		!result.ExpiresAt.Equal(v.ExpiresAt) || result.FinishedAt == nil ||
		result.FinishedAt.IsZero() || result.FinishedAt.Before(v.CreatedAt) ||
		result.FinishedAt.After(v.ExpiresAt) {
		return fmt.Errorf("%w: verification result identity mismatch", ErrUnavailable)
	}
	switch result.Status {
	case protocol.VerificationPassed, protocol.VerificationFailed, protocol.VerificationTimedOut,
		protocol.VerificationCancelled, protocol.VerificationError, protocol.VerificationSourceChanged:
	default:
		return fmt.Errorf("%w: verification result is not terminal", ErrUnavailable)
	}
	if result.Status == protocol.VerificationPassed && (result.ExitCode == nil || *result.ExitCode != 0) {
		return fmt.Errorf("%w: passed verification has invalid exit", ErrUnavailable)
	}
	if len(result.Output) > protocol.IntegrationMaxOutputBytes {
		return fmt.Errorf("%w: verification result output exceeds bound", ErrUnavailable)
	}
	if len(result.Error) > verificationResultMaxErrorBytes {
		return fmt.Errorf("%w: verification result error exceeds bound", ErrUnavailable)
	}
	return nil
}
