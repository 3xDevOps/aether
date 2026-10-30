package sshd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type launchKey struct {
	member domain.MemberID
	key    string
}

type launchKeyLock struct {
	mu   sync.Mutex
	refs int
}

// idempotentLaunch runs launch once per (member, key): a retry, including
// one racing the first, gets the run the first started, replayed. The
// fingerprint ties the key to method and params, so a key reused for a
// different launch is refused rather than answered with an unrelated run.
// The receipt is reserved before launch and bound to the run as soon as its
// row is durable, through the created callback launch passes on as
// domain.LaunchOptions.Created; a launch that fails after that is replayed
// as the run it left. The key's lock is held only until then, so a retry
// during the run's provisioning returns the run at once. A reservation with
// no run is a launch that failed or stopped before creating one, and a
// retry launches again. An empty key launches every time.
func (s *Server) idempotentLaunch(ctx context.Context, member domain.MemberID, key, method string, params any, launch func(created func(domain.RunID)) (*domain.Run, *protocol.Error)) (*domain.Run, bool, *protocol.Error) {
	if key == "" {
		run, perr := launch(nil)
		return run, false, perr
	}
	if len(key) > protocol.CoordMaxIdempotencyKeyBytes {
		return nil, false, invalidParams("idempotency_key is too long")
	}
	fingerprint, err := launchFingerprint(method, params)
	if err != nil {
		return nil, false, rpcError(err)
	}
	unlock := sync.OnceFunc(s.lockLaunchKey(launchKey{member, key}))
	defer unlock()
	prior, recorded, err := s.cfg.Store.LaunchReceipt(ctx, member, key)
	switch {
	case err == nil && recorded != fingerprint:
		return nil, false, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("%s: idempotency_key %q was used for a launch with other params; use a new key for a new launch", method, key)}
	case err == nil && prior != "":
		run, getErr := s.cfg.Store.GetRun(ctx, prior)
		if errors.Is(getErr, store.ErrNotFound) {
			return nil, false, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: session %s launched under idempotency_key %q was deleted", method, prior, key)}
		}
		if getErr != nil {
			return nil, false, rpcError(getErr)
		}
		return run, true, nil
	case errors.Is(err, store.ErrNotFound):
		if reserveErr := s.cfg.Store.ReserveLaunchReceipt(ctx, member, key, fingerprint); reserveErr != nil {
			return nil, false, rpcError(fmt.Errorf("%s: reserve idempotency_key %q: %w", method, key, reserveErr))
		}
	case err != nil:
		return nil, false, rpcError(err)
	}
	var started domain.RunID
	var bindErr error
	bind := func(id domain.RunID) {
		started, bindErr = id, s.cfg.Store.CompleteLaunchReceipt(context.WithoutCancel(ctx), member, key, id)
		if bindErr == nil {
			unlock()
		}
	}
	run, perr := launch(bind)
	if started == "" && run != nil {
		bind(run.ID)
	}
	if bindErr != nil {
		return nil, false, rpcError(fmt.Errorf("%s: session %s started, but recording it under idempotency_key %q failed, so a retry with this key launches again: %w", method, started, key, bindErr))
	}
	if perr != nil {
		return nil, false, perr
	}
	return run, false, nil
}

func launchFingerprint(method string, params any) (string, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("fingerprint launch params: %w", err)
	}
	sum := sha256.Sum256(append([]byte(method+"\x00"), b...))
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) lockLaunchKey(k launchKey) func() {
	s.launchKeysMu.Lock()
	if s.launchKeys == nil {
		s.launchKeys = make(map[launchKey]*launchKeyLock)
	}
	l := s.launchKeys[k]
	if l == nil {
		l = &launchKeyLock{}
		s.launchKeys[k] = l
	}
	l.refs++
	s.launchKeysMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.launchKeysMu.Lock()
		defer s.launchKeysMu.Unlock()
		if l.refs--; l.refs == 0 {
			delete(s.launchKeys, k)
		}
	}
}
