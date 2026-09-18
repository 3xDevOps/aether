package gitengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	CandidateDeliveryUpdateRef = "update_ref"
	CandidateDeliveryProposal  = "proposal"
)

type candidateReceiptRecord struct {
	RequestID        string `json:"request_id"`
	Action           string `json:"action"`
	TargetRef        string `json:"target_ref"`
	ExpectedRevision string `json:"expected_revision"`
	Revision         string `json:"revision"`
	Result           string `json:"result"`
	ProposalRef      string `json:"proposal_ref,omitempty"`
	PreviousRevision string `json:"previous_revision,omitempty"`
}

func candidateReceipt(r candidateReceiptRecord) CandidateGitReceipt {
	return CandidateGitReceipt{Result: r.Result, ProposalRef: r.ProposalRef, PreviousRevision: r.PreviousRevision, Revision: r.Revision}
}

func validateCandidateReceiptBinding(r candidateReceiptRecord, requestID, action, targetRef, expected, revision string) error {
	if r.RequestID != requestID || r.Action != action || r.TargetRef != targetRef || r.ExpectedRevision != expected || r.Revision != revision {
		return ErrCandidateReceiptConflict
	}
	if action == CandidateDeliveryUpdateRef && (r.Result != "landed" || r.ProposalRef != "") {
		return ErrCandidateReceiptConflict
	}
	if action == CandidateDeliveryProposal && (r.Result != "proposed" || r.ProposalRef != "refs/heads/aether/proposal-"+requestID) {
		return ErrCandidateReceiptConflict
	}
	return nil
}

func candidateTargetRef(target string) error {
	if !strings.HasPrefix(target, "refs/heads/") || target == "refs/heads/" {
		return errors.New("gitengine: delivery target must be a full heads ref")
	}
	return validateBranchName(strings.TrimPrefix(target, "refs/heads/"))
}

func (e *Engine) readCandidateReceipt(ctx context.Context, repo, ref string) (candidateReceiptRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return candidateReceiptRecord{}, false, err
	}
	if _, err := e.git(ctx, repo, "show-ref", "--verify", "--quiet", ref); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return candidateReceiptRecord{}, false, nil
		}
		return candidateReceiptRecord{}, false, err
	}
	kind, err := e.git(ctx, repo, "cat-file", "-t", ref)
	if err != nil {
		return candidateReceiptRecord{}, false, err
	}
	if strings.TrimSpace(kind) != "blob" {
		return candidateReceiptRecord{}, false, fmt.Errorf("gitengine: malformed candidate receipt")
	}
	data, err := e.git(ctx, repo, "cat-file", "blob", ref)
	if err != nil {
		return candidateReceiptRecord{}, false, err
	}
	var record candidateReceiptRecord
	if err := json.Unmarshal([]byte(data), &record); err != nil {
		return candidateReceiptRecord{}, false, fmt.Errorf("gitengine: decode candidate receipt: %w", err)
	}
	if record.RequestID == "" || record.Action == "" || record.TargetRef == "" || !validObjectID(record.Revision) {
		return candidateReceiptRecord{}, false, fmt.Errorf("gitengine: malformed candidate receipt")
	}
	return record, true, nil

}

// LookupCandidateDelivery returns the exact private receipt without requiring
// the candidate, verification, approval, or target to remain current.
func (e *Engine) LookupCandidateDelivery(ctx context.Context, ws domain.WorkspaceID, id, requestID, action, targetRef, expected, revision string) (CandidateGitReceipt, bool, error) {
	if err := ctx.Err(); err != nil {
		return CandidateGitReceipt{}, false, err
	}
	if err := validateID(id); err != nil {
		return CandidateGitReceipt{}, false, err
	}
	if err := validateID(requestID); err != nil {
		return CandidateGitReceipt{}, false, err
	}
	if action != CandidateDeliveryUpdateRef && action != CandidateDeliveryProposal {
		return CandidateGitReceipt{}, false, errors.New("gitengine: unsupported candidate delivery action")
	}
	if err := candidateTargetRef(targetRef); err != nil {
		return CandidateGitReceipt{}, false, err
	}
	if !validObjectID(revision) {
		return CandidateGitReceipt{}, false, candidateObjectError("delivery revision", revision)
	}
	if expected != "" && !validObjectID(expected) {
		return CandidateGitReceipt{}, false, candidateObjectError("expected target revision", expected)
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return CandidateGitReceipt{}, false, err
	}
	record, found, err := e.readCandidateReceipt(ctx, repo, candidateReceiptRef(id, requestID))
	if err != nil || !found {
		return CandidateGitReceipt{}, found, err
	}
	if err := validateCandidateReceiptBinding(record, requestID, action, targetRef, expected, revision); err != nil {
		return CandidateGitReceipt{}, true, err
	}
	return candidateReceipt(record), true, nil
}

func (e *Engine) candidateMirrorTarget(ctx context.Context, repo, targetRef string) (bool, error) {
	base, _, err := e.configuredMirror(ctx, repo)
	if err == nil {
		return base == targetRef, nil
	}
	if _, keyErr := e.git(ctx, repo, "config", "--get", mirrorBaseConfig); keyErr != nil {
		var exitErr *exec.ExitError
		if errors.As(keyErr, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("gitengine: mirror policy lookup failed: %w", keyErr)
	}
	return false, fmt.Errorf("gitengine: mirror policy is malformed: %w", err)
}

func (e *Engine) candidateReceiptBlob(ctx context.Context, repo string, record candidateReceiptRecord) (string, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	hash, err := e.gitInput(ctx, repo, gitEnv(), data, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	hash = strings.TrimSpace(hash)
	if !validObjectID(hash) {
		return "", errors.New("gitengine: receipt blob has invalid object id")
	}
	return hash, nil
}

// DeliverCandidate performs an atomic target/ref plus private receipt update.
// A retry is resolved exclusively through the receipt metadata, not by
// comparing the current target with the requested revision.
func (e *Engine) DeliverCandidate(ctx context.Context, ws domain.WorkspaceID, id, requestID, action, targetRef, expected, revision string) (CandidateGitReceipt, error) {
	if err := ctx.Err(); err != nil {
		return CandidateGitReceipt{}, err
	}
	if err := validateID(id); err != nil {
		return CandidateGitReceipt{}, err
	}
	if err := validateID(requestID); err != nil {
		return CandidateGitReceipt{}, err
	}
	if action != CandidateDeliveryUpdateRef && action != CandidateDeliveryProposal {
		return CandidateGitReceipt{}, errors.New("gitengine: unsupported candidate delivery action")
	}
	if err := candidateTargetRef(targetRef); err != nil {
		return CandidateGitReceipt{}, err
	}
	if !validObjectID(revision) {
		return CandidateGitReceipt{}, candidateObjectError("delivery revision", revision)
	}
	if expected != "" && !validObjectID(expected) {
		return CandidateGitReceipt{}, candidateObjectError("expected target revision", expected)
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return CandidateGitReceipt{}, err
	}
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	receiptRef := candidateReceiptRef(id, requestID)
	if prior, found, receiptErr := e.readCandidateReceipt(ctx, repo, receiptRef); receiptErr != nil {
		return CandidateGitReceipt{}, receiptErr
	} else if found {
		if bindingErr := validateCandidateReceiptBinding(prior, requestID, action, targetRef, expected, revision); bindingErr != nil {
			return CandidateGitReceipt{}, bindingErr
		}
		return candidateReceipt(prior), nil
	}
	mirrored, mirrorErr := e.candidateMirrorTarget(ctx, repo, targetRef)
	if mirrorErr != nil {
		return CandidateGitReceipt{}, mirrorErr
	}
	if checkErr := e.checkCandidate(ctx, ws, id, revision); checkErr != nil {
		return CandidateGitReceipt{}, checkErr
	}
	if action == CandidateDeliveryUpdateRef && mirrored {
		return CandidateGitReceipt{}, errors.New("gitengine: mirrored workspace target is not directly deliverable")
	}
	previous := ""
	if got, revParseErr := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", targetRef); revParseErr == nil {
		previous = got
	} else if expected != "" {
		return CandidateGitReceipt{}, fmt.Errorf("gitengine: expected target is unavailable")
	}
	if expected != previous {
		return CandidateGitReceipt{}, fmt.Errorf("gitengine: target changed since request")
	}
	if _, catFileErr := e.git(ctx, repo, "cat-file", "-e", revision+"^{commit}"); catFileErr != nil {
		return CandidateGitReceipt{}, catFileErr
	}
	proposalRef := ""
	result := "landed"
	if action == CandidateDeliveryProposal {
		proposalRef = "refs/heads/aether/proposal-" + requestID
		result = "proposed"
		if existing, eErr := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", proposalRef); eErr == nil && existing != revision {
			return CandidateGitReceipt{}, ErrCandidateReceiptConflict
		}
	}
	record := candidateReceiptRecord{RequestID: requestID, Action: action, TargetRef: targetRef, ExpectedRevision: expected, Revision: revision, Result: result, ProposalRef: proposalRef, PreviousRevision: previous}
	blob, err := e.candidateReceiptBlob(ctx, repo, record)
	if err != nil {
		return CandidateGitReceipt{}, err
	}
	zero := strings.Repeat("0", len(revision))
	if expected == "" {
		format, ferr := e.git(ctx, repo, "rev-parse", "--show-object-format")
		if ferr == nil && strings.TrimSpace(format) == "sha256" {
			zero = strings.Repeat("0", 64)
		} else if ferr == nil {
			zero = strings.Repeat("0", 40)
		}
	} else {
		zero = strings.Repeat("0", len(expected))
	}
	transaction := "start\n"
	if action == CandidateDeliveryUpdateRef {
		transaction += "update " + targetRef + " " + revision + " " + expectedOrZero(expected, zero) + "\n"
	} else {
		transaction += "verify " + targetRef + " " + expectedOrZero(expected, zero) + "\ncreate " + proposalRef + " " + revision + "\n"
	}
	transaction += "create " + receiptRef + " " + blob + "\nprepare\ncommit\n"
	if _, err := e.gitInput(ctx, repo, gitEnv(), []byte(transaction), "update-ref", "--stdin"); err != nil {
		if prior, found, rErr := e.readCandidateReceipt(ctx, repo, receiptRef); rErr == nil && found && validateCandidateReceiptBinding(prior, requestID, action, targetRef, expected, revision) == nil {
			return candidateReceipt(prior), nil
		}
		return CandidateGitReceipt{}, fmt.Errorf("gitengine: deliver candidate: %w", err)
	}
	return candidateReceipt(record), nil
}

func expectedOrZero(expected, zero string) string {
	if expected == "" {
		return zero
	}
	return expected
}

// RemoveCandidate removes only candidate-private refs and server-owned
// checkouts. Public proposal refs are intentionally retained for transport.
func (e *Engine) RemoveCandidate(ctx context.Context, ws domain.WorkspaceID, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	repo, repoErr := e.existingRepoPath(ws)
	repoMissing := false
	if repoErr != nil {
		if !errors.Is(repoErr, ErrRepoNotFound) {
			return repoErr
		}
		repoPath, pathErr := e.repoPath(ws)
		if pathErr != nil {
			return pathErr
		}
		if _, statErr := os.Lstat(repoPath); !errors.Is(statErr, os.ErrNotExist) {
			return repoErr
		}
		repoMissing = true
	}
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	checkout, err := e.candidatePath(id)
	if err != nil {
		return err
	}
	metaPath, err := e.candidateMetaPath(id)
	if err != nil {
		return err
	}
	verificationRoot := filepath.Join(e.cfg.CheckoutsDir, "verifications", id)
	scratchRoot := filepath.Join(e.cfg.CheckoutsDir, "verification-git", id)
	if repoMissing {
		if removeCheckoutErr := os.RemoveAll(checkout); removeCheckoutErr != nil {
			return removeCheckoutErr
		}
		if removeMetaErr := os.Remove(metaPath); removeMetaErr != nil && !errors.Is(removeMetaErr, os.ErrNotExist) {
			return removeMetaErr
		}
		if removeVerificationErr := os.RemoveAll(verificationRoot); removeVerificationErr != nil {
			return removeVerificationErr
		}
		return os.RemoveAll(scratchRoot)
	}
	if meta, mErr := e.loadCandidateMeta(id); mErr == nil && meta.Workspace != ws {
		return fmt.Errorf("gitengine: candidate workspace mismatch")
	}
	if removeCheckoutErr := os.RemoveAll(checkout); removeCheckoutErr != nil {
		return removeCheckoutErr
	}
	if removeMetaErr := os.Remove(metaPath); removeMetaErr != nil && !errors.Is(removeMetaErr, os.ErrNotExist) {
		return removeMetaErr
	}
	if removeVerificationErr := os.RemoveAll(verificationRoot); removeVerificationErr != nil {
		return removeVerificationErr
	}
	if removeScratchErr := os.RemoveAll(scratchRoot); removeScratchErr != nil {
		return removeScratchErr
	}
	refs, err := e.git(ctx, repo, "for-each-ref", "--format=%(refname)", candidateRefRoot+id+"/")
	if err != nil {
		return err
	}
	for ref := range strings.SplitSeq(refs, "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, err := e.git(ctx, repo, "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return nil
}
