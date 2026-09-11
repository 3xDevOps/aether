package gitengine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// MirrorFetchFunc is a test-only transport seam. Production callers leave it
// nil, which selects the hardened git fetch implementation below. A seam is
// deliberately explicit: it is the only way tests may use a local-file
// remote; production mirror validation never permits that protocol.
type MirrorFetchFunc func(ctx context.Context, repo string, req MirrorRequest, incomingRef string) error

// MirrorResolveFunc is a narrow test seam for public HTTPS DNS lookups.
// Production callers leave it nil, selecting the system resolver. Deploy-key
// SSH fetches never invoke this seam.
type MirrorResolveFunc func(ctx context.Context, host string) ([]net.IP, error)

// MirrorRequest describes one upstream observation. Private key bytes never
// cross this API; deploy-key authentication refers to a file path owned by
// the server's credential lifecycle.
type MirrorRequest struct {
	SourceURL      string
	Branch         string
	Generation     int64
	Auth           domain.MirrorAuth
	PrivateKeyPath string
	KnownHostsPath string
}

// MirrorResult reports the upstream observation and refs accepted into the
// workspace. CheckedAt is when this result was produced.
type MirrorResult struct {
	WorkspaceID     domain.WorkspaceID
	SourceURL       string
	Branch          string
	Generation      int64
	Status          domain.MirrorStatus
	PreviousCommit  string
	ObservedCommit  string
	AcceptedCommit  string
	CandidateCommit string
	BaseCommit      string
	Changed         bool
	CheckedAt       time.Time
}

// MirrorErrorKind classifies a failed or intentionally deferred observation.
type MirrorErrorKind string

const (
	MirrorErrorInvalidRequest MirrorErrorKind = "invalid-request"
	MirrorErrorNotConfigured  MirrorErrorKind = "not-configured"
	MirrorErrorAuthFailed     MirrorErrorKind = "auth-failed"
	MirrorErrorOffline        MirrorErrorKind = "offline"
	MirrorErrorSourceMissing  MirrorErrorKind = "source-missing"
	MirrorErrorRewritten      MirrorErrorKind = "rewritten"
	MirrorErrorDiverged       MirrorErrorKind = "diverged"
	MirrorErrorCASConflict    MirrorErrorKind = "cas-conflict"
	MirrorErrorNoCandidate    MirrorErrorKind = "no-candidate"
	MirrorErrorUnsupported    MirrorErrorKind = "unsupported"
	MirrorErrorFailed         MirrorErrorKind = "error"
)

// MirrorError is safe to show to an operator: it does not include git's raw
// stderr, which can contain credential-helper or transport details. Cause is
// retained for errors.Is/As by server code but is intentionally omitted from
// Error().
type MirrorError struct {
	Kind        MirrorErrorKind
	WorkspaceID domain.WorkspaceID
	SourceURL   string
	Branch      string
	Base        string
	Observed    string
	Cause       error
}

func (e *MirrorError) Error() string {
	if e == nil {
		return "gitengine: mirror error"
	}
	what := string(e.Kind)
	if what == "" {
		what = string(MirrorErrorFailed)
	}
	if e.Branch != "" {
		return fmt.Sprintf("gitengine: mirror %s for branch %q", what, e.Branch)
	}
	return "gitengine: mirror " + what
}

func (e *MirrorError) Unwrap() error { return e.Cause }

const (
	mirrorBaseConfig             = "aether.mirror.base"
	mirrorGenerationConfig       = "aether.mirror.generation" // durable high-water mark
	mirrorActiveGenerationConfig = "aether.mirror.active-generation"
	mirrorRefPrefix              = "refs/aether/mirror/"
	mirrorMaxCASAttempts         = 3
	zeroObjectID                 = "0000000000000000000000000000000000000000"
)

var mirrorSharedCGNAT = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

// ConfigureWorkspaceMirror installs the protected-base policy and records the
// exact mirrored ref and generation. It intentionally does not fetch refs;
// persistence and key lifecycle belong to the service layer. The generation
// high-water mark is never lowered, including when a prior configuration is
// rolled back.
func (e *Engine) ConfigureWorkspaceMirror(ctx context.Context, ws domain.WorkspaceID, req MirrorRequest) (MirrorResult, error) {
	result := MirrorResult{WorkspaceID: ws, SourceURL: req.SourceURL, Branch: req.Branch, Generation: req.Generation, CheckedAt: time.Now().UTC()}
	if err := validateMirrorRequest(req, e.cfg.MirrorFetch != nil); err != nil {
		return result, mirrorErr(MirrorErrorInvalidRequest, ws, req, "", "", err)
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return result, err
	}
	previousGeneration := int64(0)
	if _, configuredGen, configuredErr := e.configuredMirror(ctx, repo); configuredErr == nil {
		previousGeneration = configuredGen
	}
	durable, err := e.MirrorGeneration(ctx, ws)
	if err != nil {
		return result, err
	}
	// The active generation may be restored after a persistence failure, but
	// the durable high-water mark is never lowered. Service allocates the
	// next value for every new configuration.
	if err := e.configureWorkspaceRepo(ctx, repo); err != nil {
		return result, err
	}
	base := mirrorBaseRef(req.Branch)
	if req.Generation > durable {
		if _, err := e.git(ctx, repo, "config", mirrorGenerationConfig, strconv.FormatInt(req.Generation, 10)); err != nil {
			return result, err
		}
	}
	if _, err := e.git(ctx, repo, "config", mirrorActiveGenerationConfig, strconv.FormatInt(req.Generation, 10)); err != nil {
		return result, err
	}
	if _, err := e.git(ctx, repo, "config", mirrorBaseConfig, base); err != nil {
		return result, err
	}
	// Do not remove an old generation until the complete new policy is
	// installed. A failed configuration therefore leaves the old refs
	// available for rollback, while a successful reconfigure makes stale
	// candidates unadoptable before they are removed.
	if previousGeneration > 0 && previousGeneration != req.Generation {
		accepted, candidate := mirrorRefs(previousGeneration)
		if err := e.deleteMirrorRefs(ctx, repo, accepted, candidate); err != nil {
			return result, err
		}
	}
	result.BaseCommit = readRefBestEffort(ctx, e, repo, base)
	result.PreviousCommit = result.BaseCommit
	result.Status = mirrorStatusForRefs(result.BaseCommit, "", "", "")
	return result, nil
}

// MirrorGeneration returns the durable generation high-water mark for the
// workspace. It remains after Disable removes the active policy, so a later
// configuration can allocate the next value.
func (e *Engine) MirrorGeneration(ctx context.Context, ws domain.WorkspaceID) (int64, error) {
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return 0, err
	}
	value, err := e.git(ctx, repo, "config", "--get", mirrorGenerationConfig)
	if err != nil || strings.TrimSpace(value) == "" {
		// Repositories written before the separate active-generation key was
		// introduced used this key as the active policy generation.
		value, err = e.git(ctx, repo, "config", "--get", mirrorActiveGenerationConfig)
		if err != nil || strings.TrimSpace(value) == "" {
			return 0, nil
		}
	}
	generation, parseErr := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if parseErr != nil || generation < 0 {
		return 0, fmt.Errorf("gitengine: invalid durable mirror generation")
	}
	return generation, nil
}

// DisableWorkspaceMirror removes the mirrored-base policy and its accepted /
// candidate refs. The workspace branch itself is left intact and becomes a
// normal client-writable branch again. The always-on refs/aether hiding policy
// remains in place for server-owned bookkeeping refs.
func (e *Engine) DisableWorkspaceMirror(ctx context.Context, ws domain.WorkspaceID) error {
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return err
	}
	generation, _ := e.git(ctx, repo, "config", "--get", mirrorActiveGenerationConfig)
	if generation == "" {
		// Legacy repositories used the durable key for the active policy.
		generation, _ = e.git(ctx, repo, "config", "--get", mirrorGenerationConfig)
	}
	var refs []string
	if generation != "" {
		if gen, parseErr := strconv.ParseInt(strings.TrimSpace(generation), 10, 64); parseErr == nil && gen >= 0 {
			// Migrate a legacy active generation before removing its policy.
			if value, getErr := e.git(ctx, repo, "config", "--get", mirrorGenerationConfig); getErr != nil || strings.TrimSpace(value) == "" {
				if _, setErr := e.git(ctx, repo, "config", mirrorGenerationConfig, strconv.FormatInt(gen, 10)); setErr != nil {
					return setErr
				}
			}
			accepted, candidate := mirrorRefs(gen)
			refs = []string{accepted, candidate}
		}
	}

	// Remove policy before the final ref transaction. If the transaction
	// fails, its atomicity leaves both refs intact for service-level restore.
	for _, key := range []string{mirrorActiveGenerationConfig, mirrorBaseConfig} {
		if _, unsetErr := e.git(ctx, repo, "config", "--unset-all", key); unsetErr != nil {
			// An absent key is already the requested state. Verify before
			// suppressing the command's nonzero status so real config failures
			// remain visible.
			if _, stillSet := e.git(ctx, repo, "config", "--get", key); stillSet == nil {
				return unsetErr
			}
		}
	}
	if len(refs) > 0 {
		// This is intentionally the last fallible operation.
		return e.deleteMirrorRefs(ctx, repo, refs...)
	}
	return nil
}

// RefreshWorkspaceMirror fetches exactly req.Branch and advances the mirrored
// base only when the accepted observation is unchanged locally and upstream is
// equal to or ahead of it. Rewrites and local/server-ahead divergence retain
// candidate while leaving accepted and base untouched.
func (e *Engine) RefreshWorkspaceMirror(ctx context.Context, ws domain.WorkspaceID, req MirrorRequest) (MirrorResult, error) {
	result := MirrorResult{WorkspaceID: ws, SourceURL: req.SourceURL, Branch: req.Branch, Generation: req.Generation, CheckedAt: time.Now().UTC()}
	if err := validateMirrorRequest(req, e.cfg.MirrorFetch != nil); err != nil {
		return result, mirrorErr(MirrorErrorInvalidRequest, ws, req, "", "", err)
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return result, err
	}
	configuredBase, configuredGen, err := e.configuredMirror(ctx, repo)
	if err != nil {
		return result, mirrorErr(MirrorErrorNotConfigured, ws, req, "", "", err)
	}
	wantBase := mirrorBaseRef(req.Branch)
	if configuredBase != wantBase || configuredGen != req.Generation {
		return result, mirrorErr(MirrorErrorInvalidRequest, ws, req, "", "", fmt.Errorf("mirror policy does not match request"))
	}
	acceptedRef, candidateRef := mirrorRefs(req.Generation)
	incomingRef, err := newIncomingRef()
	if err != nil {
		return result, mirrorErr(MirrorErrorFailed, ws, req, "", "", err)
	}
	if err := e.fetchMirror(ctx, repo, req, incomingRef); err != nil {
		_ = e.deleteMirrorRefs(ctx, repo, incomingRef)
		kind := classifyFetchError(err)
		result.Status = mirrorStatusForError(kind)
		return result, mirrorErr(kind, ws, req, "", "", err)
	}
	defer func() { _ = e.deleteMirrorRefs(context.Background(), repo, incomingRef) }()
	for attempt := range mirrorMaxCASAttempts {
		base := readRefBestEffort(ctx, e, repo, configuredBase)
		accepted := readRefBestEffort(ctx, e, repo, acceptedRef)
		candidate := readRefBestEffort(ctx, e, repo, candidateRef)
		observed := readRefBestEffort(ctx, e, repo, incomingRef)
		result.PreviousCommit = base
		result.ObservedCommit = observed
		result.AcceptedCommit = accepted
		result.CandidateCommit = candidate
		result.BaseCommit = base
		status, nextAccepted, nextBase := e.classifyMirror(ctx, repo, base, accepted, observed)
		result.Status = status
		result.Changed = candidate != observed || nextAccepted != accepted || nextBase != base
		if status == domain.MirrorStatusRewritten || status == domain.MirrorStatusDiverged {
			// Candidate is retained even when the source cannot be accepted.
			nextAccepted, nextBase = accepted, base
		}
		if err := e.commitMirrorObservation(ctx, repo, configuredBase, acceptedRef, candidateRef, incomingRef, base, accepted, candidate, observed, nextBase, nextAccepted); err != nil {
			if attempt+1 < mirrorMaxCASAttempts {
				continue
			}
			result.Status = domain.MirrorStatusError
			return result, mirrorErr(MirrorErrorCASConflict, ws, req, base, observed, err)
		}
		result.AcceptedCommit, result.BaseCommit, result.CandidateCommit = nextAccepted, nextBase, observed
		if status == domain.MirrorStatusRewritten {
			return result, mirrorErr(MirrorErrorRewritten, ws, req, base, observed, nil)
		}
		if status == domain.MirrorStatusDiverged {
			return result, mirrorErr(MirrorErrorDiverged, ws, req, base, observed, nil)
		}
		return result, nil
	}
	return result, mirrorErr(MirrorErrorCASConflict, ws, req, result.BaseCommit, result.ObservedCommit, nil)
}

// AdoptWorkspaceMirror explicitly accepts a retained candidate and moves it to
// accepted and the mirrored base. This is the sole non-fast-forward operation.
func (e *Engine) AdoptWorkspaceMirror(ctx context.Context, ws domain.WorkspaceID, generation int64) (MirrorResult, error) {
	result := MirrorResult{WorkspaceID: ws, Generation: generation, CheckedAt: time.Now().UTC()}
	if generation < 0 {
		return result, mirrorErr(MirrorErrorInvalidRequest, ws, MirrorRequest{Generation: generation}, "", "", errors.New("invalid mirror generation"))
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return result, err
	}
	baseRef, configuredGen, err := e.configuredMirror(ctx, repo)
	if err != nil || configuredGen != generation {
		if err == nil {
			err = fmt.Errorf("mirror generation does not match policy")
		}
		return result, mirrorErr(MirrorErrorNotConfigured, ws, MirrorRequest{Generation: generation}, "", "", err)
	}
	branch := strings.TrimPrefix(baseRef, "refs/heads/")
	acceptedRef, candidateRef := mirrorRefs(generation)
	for attempt := range mirrorMaxCASAttempts {
		base := readRefBestEffort(ctx, e, repo, baseRef)
		accepted := readRefBestEffort(ctx, e, repo, acceptedRef)
		candidate := readRefBestEffort(ctx, e, repo, candidateRef)
		result := MirrorResult{
			WorkspaceID:     ws,
			Branch:          branch,
			Generation:      generation,
			PreviousCommit:  base,
			ObservedCommit:  candidate,
			AcceptedCommit:  accepted,
			CandidateCommit: candidate,
			BaseCommit:      base,
			CheckedAt:       time.Now().UTC(),
			Status:          domain.MirrorStatusReady,
		}
		if candidate == "" {
			result.Status = domain.MirrorStatusPending
			return result, mirrorErr(MirrorErrorNoCandidate, ws, MirrorRequest{Branch: branch, Generation: generation}, base, candidate, nil)
		}
		if err := e.commitMirrorObservation(ctx, repo, baseRef, acceptedRef, candidateRef, "", base, accepted, candidate, candidate, candidate, candidate); err != nil {
			if attempt+1 < mirrorMaxCASAttempts {
				continue
			}
			result.Status = domain.MirrorStatusError
			return result, mirrorErr(MirrorErrorCASConflict, ws, MirrorRequest{Branch: branch, Generation: generation}, base, candidate, err)
		}
		result.AcceptedCommit, result.BaseCommit, result.CandidateCommit = candidate, candidate, candidate
		result.Changed = base != candidate || accepted != candidate
		return result, nil
	}
	return MirrorResult{WorkspaceID: ws, Generation: generation, CheckedAt: time.Now().UTC(), Status: domain.MirrorStatusError}, mirrorErr(MirrorErrorCASConflict, ws, MirrorRequest{Generation: generation}, "", "", nil)
}

// WorkspaceBranchCommit returns the full object id at a normal workspace
// branch. Reads intentionally remain available for mirrored bases and run
// branches; hidden refs/aether bookkeeping is not part of this API.
func (e *Engine) WorkspaceBranchCommit(ctx context.Context, ws domain.WorkspaceID, branch string) (string, error) {
	ref, err := workspaceBranchRef(branch)
	if err != nil {
		return "", err
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return "", err
	}
	commit := readRefBestEffort(ctx, e, repo, ref)
	if commit == "" {
		return "", fmt.Errorf("gitengine: workspace branch %q has no commit", branch)
	}
	return commit, nil
}

func validateMirrorRequest(req MirrorRequest, seam bool) error {
	if strings.TrimSpace(req.SourceURL) == "" {
		return errors.New("source URL is required")
	}
	if err := validateBranchName(req.Branch); err != nil {
		return err
	}
	if req.Generation < 0 {
		return errors.New("generation must not be negative")
	}
	switch req.Auth {
	case domain.MirrorAuthPublic:
		u, err := url.Parse(req.SourceURL)
		if !seam {
			if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil {
				return errors.New("public mirror source must be credential-free HTTPS")
			}
			if port := u.Port(); port != "" && port != "443" {
				return errors.New("public mirror source must use HTTPS port 443")
			}
		}
	case domain.MirrorAuthDeployKey:
		u, err := url.Parse(req.SourceURL)
		if err != nil || u.Scheme != "ssh" || u.Host == "" || u.Hostname() == "" {
			return errors.New("deploy-key mirror source must use ssh://")
		}
		if u.User != nil {
			if _, hasPassword := u.User.Password(); hasPassword {
				return errors.New("deploy-key mirror source must not contain a password")
			}
		}
		if req.PrivateKeyPath == "" || req.KnownHostsPath == "" || !filepath.IsAbs(req.PrivateKeyPath) || !filepath.IsAbs(req.KnownHostsPath) {
			return errors.New("deploy-key mirror requires absolute private-key and known-hosts paths")
		}
	default:
		return errors.New("unknown mirror authentication")
	}
	return nil
}

func validateBranchName(branch string) error {
	if strings.TrimSpace(branch) == "" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") ||
		strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, "/") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") ||
		strings.Contains(branch, "//") {
		return errors.New("invalid mirror branch")
	}
	for _, r := range branch {
		if r <= ' ' || strings.ContainsRune("~^:?*[\\", r) {
			return errors.New("invalid mirror branch")
		}
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return errors.New("invalid mirror branch")
		}
	}
	return nil
}
func mirrorBaseRef(branch string) string { return "refs/heads/" + branch }
func mirrorRefs(gen int64) (string, string) {
	prefix := mirrorRefPrefix + strconv.FormatInt(gen, 10)
	return prefix + "/accepted", prefix + "/candidate"
}

func newIncomingRef() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "refs/aether/incoming/" + hex.EncodeToString(buf[:]), nil
}

func (e *Engine) configuredMirror(ctx context.Context, repo string) (string, int64, error) {
	base, err := e.git(ctx, repo, "config", "--get", mirrorBaseConfig)
	if err != nil || base == "" {
		return "", 0, errors.New("mirror is not configured")
	}
	generation, err := e.git(ctx, repo, "config", "--get", mirrorActiveGenerationConfig)
	if err != nil || strings.TrimSpace(generation) == "" {
		// Repositories written before the active-generation key was added
		// used the durable key as their active policy generation.
		generation, err = e.git(ctx, repo, "config", "--get", mirrorGenerationConfig)
	}
	if err != nil {
		return "", 0, errors.New("mirror generation is not configured")
	}
	gen, err := strconv.ParseInt(strings.TrimSpace(generation), 10, 64)
	if err != nil || gen < 0 {
		return "", 0, errors.New("invalid configured mirror generation")
	}
	if !strings.HasPrefix(base, "refs/heads/") || base == "refs/heads/" {
		return "", 0, errors.New("invalid configured mirror base")
	}
	return strings.TrimSpace(base), gen, nil
}

func (e *Engine) fetchMirror(ctx context.Context, repo string, req MirrorRequest, incomingRef string) error {
	if e.cfg.MirrorFetch != nil {
		return e.cfg.MirrorFetch(ctx, repo, req, incomingRef)
	}
	args := []string{"-C", repo, "-c", "safe.directory=*"}
	env := gitEnv()
	if req.Auth == domain.MirrorAuthPublic {
		u, err := url.Parse(req.SourceURL)
		if err != nil || u.Hostname() == "" {
			if err == nil {
				err = errors.New("public mirror source has no hostname")
			}
			return &mirrorFetchFailure{err: err, output: err.Error()}
		}
		addresses, err := e.resolveMirrorHost(ctx, u.Hostname())
		if err != nil {
			return &mirrorFetchFailure{err: err, output: err.Error()}
		}
		args = append(args, "-c", "http.followRedirects=false")
		for _, address := range addresses {
			args = append(args, "-c", "http.curloptResolve="+mirrorCurloptResolve(u.Hostname(), address))
		}
	}
	if req.Auth == domain.MirrorAuthDeployKey {
		ssh := strings.Join([]string{
			"/usr/bin/ssh", "-F", "/dev/null", "-i", shellQuoteMirror(req.PrivateKeyPath),
			"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
			"-o", shellQuoteMirror("UserKnownHostsFile=" + req.KnownHostsPath),
			"-o", "GlobalKnownHostsFile=/dev/null", "-o", "IdentityAgent=none",
		}, " ")
		env = append(env, "GIT_SSH_COMMAND="+ssh)
	}
	args = append(args, "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", req.SourceURL, "+refs/heads/"+req.Branch+":"+incomingRef)
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, args...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return &mirrorFetchFailure{err: err, output: string(out)}
	}
	return nil
}

func (e *Engine) resolveMirrorHost(ctx context.Context, host string) ([]net.IP, error) {
	var (
		addresses []net.IP
		err       error
	)
	if e.cfg.MirrorResolve != nil {
		addresses, err = e.cfg.MirrorResolve(ctx, host)
	} else {
		addresses, err = net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	if err != nil {
		return nil, fmt.Errorf("mirror DNS lookup failed: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("mirror DNS lookup returned no addresses")
	}
	for _, address := range addresses {
		if unsafeMirrorIP(address) {
			return nil, fmt.Errorf("mirror DNS lookup returned unsafe address")
		}
	}
	return addresses, nil
}

func unsafeMirrorIP(address net.IP) bool {
	if address == nil {
		return true
	}
	if v4 := address.To4(); v4 != nil {
		address = v4
	}
	return !address.IsGlobalUnicast() ||
		address.IsLoopback() ||
		address.IsPrivate() ||
		address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() ||
		address.IsMulticast() ||
		address.IsUnspecified() ||
		mirrorSharedCGNAT.Contains(address)
}

func mirrorCurloptResolve(host string, address net.IP) string {
	hostPart := host
	if strings.Contains(hostPart, ":") {
		hostPart = "[" + hostPart + "]"
	}
	addressPart := address.String()
	if address.To4() == nil {
		addressPart = "[" + addressPart + "]"
	}
	return hostPart + ":443:" + addressPart
}

type mirrorFetchFailure struct {
	err    error
	output string
}

func (e *mirrorFetchFailure) Error() string { return "mirror fetch failed" }
func (e *mirrorFetchFailure) Unwrap() error { return e.err }

func classifyFetchError(err error) MirrorErrorKind {
	var failure *mirrorFetchFailure
	if !errors.As(err, &failure) {
		return MirrorErrorFailed
	}
	msg := strings.ToLower(failure.output)
	switch {
	case strings.Contains(msg, "authentication failed"), strings.Contains(msg, "permission denied"), strings.Contains(msg, "could not read username"), strings.Contains(msg, "host key verification failed"):
		return MirrorErrorAuthFailed
	case strings.Contains(msg, "couldn't find remote ref"), strings.Contains(msg, "could not find remote ref"), strings.Contains(msg, "no such ref"):
		return MirrorErrorSourceMissing
	case strings.Contains(msg, "could not resolve host"), strings.Contains(msg, "no such host"), strings.Contains(msg, "dns lookup failed"), strings.Contains(msg, "connection refused"), strings.Contains(msg, "network is unreachable"), strings.Contains(msg, "failed to connect"), strings.Contains(msg, "unable to access"):
		return MirrorErrorOffline
	default:
		return MirrorErrorFailed
	}
}
func (e *Engine) classifyMirror(ctx context.Context, repo, base, accepted, observed string) (domain.MirrorStatus, string, string) {
	if observed == "" {
		return domain.MirrorStatusSourceMissing, accepted, base
	}
	if accepted == "" {
		if base == observed && base != "" {
			return domain.MirrorStatusReady, observed, base
		}
		if base != "" && e.isAncestorGit(ctx, repo, base, observed) {
			return domain.MirrorStatusReady, observed, observed
		}
		return domain.MirrorStatusPending, accepted, base
	}
	if base != accepted {
		return domain.MirrorStatusDiverged, accepted, base
	}
	if observed == accepted {
		return domain.MirrorStatusReady, accepted, base
	}
	if e.isAncestorGit(ctx, repo, base, observed) {
		return domain.MirrorStatusReady, observed, observed
	}
	return domain.MirrorStatusRewritten, accepted, base
}

func (e *Engine) isAncestorGit(ctx context.Context, repo, base, tip string) bool {
	if base == "" || tip == "" {
		return base == tip
	}
	_, err := e.git(ctx, repo, "merge-base", "--is-ancestor", base, tip)
	return err == nil
}
func (e *Engine) commitMirrorObservation(ctx context.Context, repo, baseRef, acceptedRef, candidateRef, incomingRef, oldBase, oldAccepted, oldCandidate, incoming, nextBase, nextAccepted string) error {
	lines := []string{"start"}
	if nextBase != oldBase {
		lines = append(lines, updateRefLine(baseRef, nextBase, oldBase))
	} else {
		lines = append(lines, verifyRefLine(baseRef, oldBase))
	}
	if nextAccepted != oldAccepted {
		lines = append(lines, updateRefLine(acceptedRef, nextAccepted, oldAccepted))
	} else {
		lines = append(lines, verifyRefLine(acceptedRef, oldAccepted))
	}
	if incomingRef != "" {
		lines = append(lines, updateRefLine(candidateRef, incoming, oldCandidate), "delete "+incomingRef+" "+incoming)
	} else {
		lines = append(lines, verifyRefLine(candidateRef, oldCandidate))
	}
	lines = append(lines, "prepare", "commit", "")
	return e.runMirrorGit(ctx, repo, strings.Join(lines, "\n"), "update-ref", "--stdin")
}

func verifyRefLine(ref, old string) string {
	if old == "" {
		old = zeroObjectID
	}
	return "verify " + ref + " " + old
}
func updateRefLine(ref, next, old string) string {
	if old == "" {
		old = zeroObjectID
	}
	if next == "" {
		return "delete " + ref + " " + old
	}
	return "update " + ref + " " + next + " " + old
}

func (e *Engine) runMirrorGit(ctx context.Context, repo, stdin string, args ...string) error {
	argv := append([]string{"-C", repo, "-c", "safe.directory=*"}, args...)
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, argv...)
	cmd.Env = gitEnv()
	cmd.Stdin = strings.NewReader(stdin)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("update-ref: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (e *Engine) deleteMirrorRefs(ctx context.Context, repo string, refs ...string) error {
	lines := []string{"start"}
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		lines = append(lines, "delete "+ref)
	}
	lines = append(lines, "prepare", "commit", "")
	return e.runMirrorGit(ctx, repo, strings.Join(lines, "\n"), "update-ref", "--stdin")
}

func readRefBestEffort(ctx context.Context, e *Engine, repo, ref string) string {
	if ref == "" {
		return ""
	}
	value, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", ref)
	if err != nil || len(value) != 40 || !isSHA(value) {
		return ""
	}
	return value
}

func workspaceBranchRef(branch string) (string, error) {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if err := validateBranchName(branch); err != nil {
		return "", err
	}
	return mirrorBaseRef(branch), nil
}

func mirrorErr(kind MirrorErrorKind, ws domain.WorkspaceID, req MirrorRequest, base, observed string, cause error) error {
	return &MirrorError{Kind: kind, WorkspaceID: ws, SourceURL: req.SourceURL, Branch: req.Branch, Base: base, Observed: observed, Cause: cause}
}

func mirrorStatusForRefs(base, accepted, observed, candidate string) domain.MirrorStatus {
	if base == "" && accepted == "" && observed == "" && candidate == "" {
		return domain.MirrorStatusPending
	}
	if base != "" && base == accepted {
		return domain.MirrorStatusReady
	}
	return domain.MirrorStatusPending
}

func mirrorStatusForError(kind MirrorErrorKind) domain.MirrorStatus {
	switch kind {
	case MirrorErrorAuthFailed:
		return domain.MirrorStatusAuthFailed
	case MirrorErrorOffline:
		return domain.MirrorStatusOffline
	case MirrorErrorSourceMissing:
		return domain.MirrorStatusSourceMissing
	case MirrorErrorRewritten:
		return domain.MirrorStatusRewritten
	case MirrorErrorDiverged:
		return domain.MirrorStatusDiverged
	default:
		return domain.MirrorStatusError
	}
}

func shellQuoteMirror(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
