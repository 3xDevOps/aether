package sshd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
// An empty key launches every time.
func (s *Server) idempotentLaunch(ctx context.Context, member domain.MemberID, key, method string, params any, launch func() (*domain.Run, *protocol.Error)) (*domain.Run, bool, *protocol.Error) {
	if key == "" {
		run, perr := launch()
		return run, false, perr
	}
	if len(key) > protocol.CoordMaxIdempotencyKeyBytes {
		return nil, false, invalidParams("idempotency_key is too long")
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, false, rpcError(fmt.Errorf("fingerprint launch params: %w", err))
	}
	sum := sha256.Sum256(append([]byte(method+"\x00"), b...))
	fingerprint := hex.EncodeToString(sum[:])
	unlock := s.lockLaunchKey(launchKey{member, key})
	defer unlock()
	prior, recorded, err := s.cfg.Store.LaunchReceipt(ctx, member, key)
	switch {
	case err == nil && recorded != fingerprint:
		return nil, false, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("%s: idempotency_key %q already launched run %s with other params; use a new key for a new launch", method, key, prior)}
	case err == nil:
		run, getErr := s.cfg.Store.GetRun(ctx, prior)
		if errors.Is(getErr, store.ErrNotFound) {
			return nil, false, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: run %s launched under idempotency_key %q was deleted", method, prior, key)}
		}
		if getErr != nil {
			return nil, false, rpcError(getErr)
		}
		return run, true, nil
	case !errors.Is(err, store.ErrNotFound):
		return nil, false, rpcError(err)
	}
	run, perr := launch()
	if perr != nil {
		return nil, false, perr
	}
	// The run started; failing the call now would invite the duplicate the
	// key exists to prevent.
	if err := s.cfg.Store.RecordLaunchReceipt(context.WithoutCancel(ctx), member, key, fingerprint, run); err != nil {
		slog.Warn("sshd: record launch receipt", "run", run.ID, "member", member, "error", err)
	}
	return run, false, nil
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
