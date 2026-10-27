// Package fallback decides which agent account a launch runs on: the account
// the launcher asked for, or, when that account's vendor usage is exhausted,
// another account the launcher may launch on.
package fallback

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/quota"
	"github.com/3xDevOps/Aether/internal/store"
)

// ExhaustedPercent is the used share of a quota window at which an account
// counts as exhausted: at 95% one long agent turn can spend the rest within
// minutes.
const ExhaustedPercent = 95

// readTimeout bounds how long a launch waits on one account's quota read. A
// slower read counts as unreadable; it carries on and fills the quota cache
// for the next launch.
const readTimeout = 3 * time.Second

// QuotaReader reads an account's vendor quota without exposing its
// credentials or the provider's responses to the caller.
type QuotaReader interface {
	Read(ctx context.Context, account domain.MemberID, refresh bool) ([]quota.Provider, error)
}

// windows are the quota windows a run of each agent draws on, keyed by agent
// and quota provider, which share a name. Claude's per-model weekly windows
// are left out because Aether does not know which model a run uses.
var windows = map[string][]string{
	"claude": {"five_hour", "seven_day"},
	"codex":  {"primary", "secondary"},
}

func Policy(order []string, disabled bool) (domain.AccountFallback, error) {
	if disabled && len(order) > 0 {
		return domain.AccountFallback{}, errors.New("fallback_account_member_ids and no_fallback cannot both be set")
	}
	policy := domain.AccountFallback{Disabled: disabled}
	for _, id := range order {
		if id == "" {
			return domain.AccountFallback{}, errors.New("fallback_account_member_ids cannot contain an empty member ID")
		}
		policy.Order = append(policy.Order, domain.MemberID(id))
	}
	return policy, nil
}

// ResolveLaunchAccount returns the account a launch by actor runs on: actor's
// own, or requested when its owner has shared their agent login with actor.
// Shares are directional. Every launch, fallback candidates included, and
// every later use of a run's account checks it, so administrators do not
// acquire implicit credential-use authority.
func ResolveLaunchAccount(ctx context.Context, st store.Store, actor domain.MemberID, requested string) (domain.MemberID, error) {
	account := domain.MemberID(requested)
	if account == "" || account == actor {
		return actor, nil
	}
	owner, err := st.GetMember(ctx, account)
	if err != nil {
		return "", err
	}
	if owner.Pending {
		return "", fmt.Errorf("%w: account owner is pending admin approval", permissions.ErrDenied)
	}
	shared, err := st.AccountSharedWith(ctx, account, actor)
	if err != nil {
		return "", err
	}
	if !shared {
		return "", fmt.Errorf("%w: member %s has not shared their account with you", permissions.ErrDenied, account)
	}
	return account, nil
}

// Measured is the highest used share of each account's quota windows, for
// the accounts whose usage could be read.
type Measured map[domain.MemberID]float64

// Measure reads the usage Select decides on: the requested account's, and,
// only when it is exhausted, every candidate's. It reads vendor quota, so
// callers run it before taking the shared authorization mutex; Select
// authorises each account again under that mutex.
func Measure(ctx context.Context, st store.Store, usage QuotaReader, launcher domain.MemberID, requested, harness string, policy domain.AccountFallback) (Measured, error) {
	if policy.Disabled || usage == nil {
		return nil, nil
	}
	account, err := ResolveLaunchAccount(ctx, st, launcher, requested)
	if err != nil {
		return nil, nil
	}
	used, ok := usedPercent(ctx, usage, account, harness)
	if !ok {
		return nil, nil
	}
	measured := Measured{account: used}
	if used < ExhaustedPercent {
		return measured, nil
	}
	if err := measureCandidates(ctx, st, usage, launcher, harness, policy, measured); err != nil {
		return nil, err
	}
	return measured, nil
}

// MeasureNext reads the usage Select decides on for a running run whose
// vendor reported its current account's usage exhausted: current and every
// account in used count as exhausted, so Select returns current when no
// other candidate qualifies.
func MeasureNext(ctx context.Context, st store.Store, usage QuotaReader, launcher, current domain.MemberID, used []domain.MemberID, harness string, policy domain.AccountFallback) (Measured, error) {
	if policy.Disabled || usage == nil {
		return nil, nil
	}
	measured := Measured{current: 100}
	for _, account := range used {
		measured[account] = 100
	}
	if err := measureCandidates(ctx, st, usage, launcher, harness, policy, measured); err != nil {
		return nil, err
	}
	return measured, nil
}

func measureCandidates(ctx context.Context, st store.Store, usage QuotaReader, launcher domain.MemberID, harness string, policy domain.AccountFallback, measured Measured) error {
	candidates, err := candidates(ctx, st, launcher, policy)
	if err != nil {
		return err
	}
	var (
		mu   sync.Mutex
		wait sync.WaitGroup
	)
	known := maps.Clone(measured)
	for _, candidate := range candidates {
		if _, ok := known[candidate]; ok {
			continue
		}
		if _, err := ResolveLaunchAccount(ctx, st, launcher, string(candidate)); err != nil {
			continue
		}
		wait.Go(func() {
			if used, ok := usedPercent(ctx, usage, candidate, harness); ok {
				mu.Lock()
				measured[candidate] = used
				mu.Unlock()
			}
		})
	}
	wait.Wait()
	return nil
}

// Launchable reports whether the launch can run on account as a fallback,
// without borrowing the owner's agent installation. It runs under the
// authorization mutex, so it reads local files only.
type Launchable func(ctx context.Context, account domain.MemberID) (bool, error)

// Select returns the account launcher's run launches on, from the usage
// Measure read. The caller has admitted launcher's launch on requested and
// holds the shared authorization mutex until the scheduler's durable launch
// boundary, so each candidate needs only its account authorization checked
// here; an account Measure could not read, or launchable refuses, is never
// chosen. A nil launchable refuses nothing. When no candidate qualifies, the
// launch stays on the requested account. A candidate whose check fails is
// left out like a refused one, so it never fails a launch that can stay on
// the requested account; excluded joins those failures.
func Select(ctx context.Context, st store.Store, measured Measured, launcher, requested domain.MemberID, policy domain.AccountFallback, launchable Launchable) (account domain.MemberID, excluded error) {
	if policy.Disabled {
		return requested, nil
	}
	if used, ok := measured[requested]; !ok || used < ExhaustedPercent {
		return requested, nil
	}
	candidates, err := candidates(ctx, st, launcher, policy)
	if err != nil {
		return requested, fmt.Errorf("list fallback accounts: %w", err)
	}
	var failures []error
	type usable struct {
		account domain.MemberID
		used    float64
	}
	var found []usable
	for _, candidate := range candidates {
		if candidate == requested {
			continue
		}
		if _, authErr := ResolveLaunchAccount(ctx, st, launcher, string(candidate)); authErr != nil {
			if !errors.Is(authErr, permissions.ErrDenied) && !errors.Is(authErr, store.ErrNotFound) {
				failures = append(failures, &Skip{Account: candidate, Reason: ReasonAccess, Err: authErr})
			}
			continue
		}
		used, ok := measured[candidate]
		if !ok || used >= ExhaustedPercent {
			continue
		}
		if launchable != nil {
			allowed, launchErr := launchable(ctx, candidate)
			if launchErr != nil {
				failures = append(failures, &Skip{Account: candidate, Reason: ReasonAgent, Err: launchErr})
				continue
			}
			if !allowed {
				continue
			}
		}
		if len(policy.Order) > 0 {
			return candidate, errors.Join(failures...)
		}
		found = append(found, usable{candidate, used})
	}
	if len(found) == 0 {
		return requested, errors.Join(failures...)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].used != found[j].used {
			return found[i].used < found[j].used
		}
		return found[i].account < found[j].account
	})
	return found[0].account, errors.Join(failures...)
}

// The reasons Skipped gives launchers for a left-out account. The errors
// behind them can name member homes and other members, so they stay in the
// server log.
const (
	ReasonAccess = "your access to the account could not be checked"
	ReasonAgent  = "the account's agent could not be checked"
	ReasonList   = "the fallback accounts could not be listed"
)

// Skip is a fallback candidate Select left out because checking it failed.
type Skip struct {
	Account domain.MemberID
	Reason  string
	Err     error
}

func (s *Skip) Error() string { return fmt.Sprintf("account %s: %v", s.Account, s.Err) }

func (s *Skip) Unwrap() error { return s.Err }

// Skipped lists what excluded, Select's second result, left out, each with
// its reason; a failure to list the candidates names no account.
func Skipped(excluded error) []protocol.SkippedAccount {
	if excluded == nil {
		return nil
	}
	errs := []error{excluded}
	if joined, ok := excluded.(interface{ Unwrap() []error }); ok {
		errs = joined.Unwrap()
	}
	out := make([]protocol.SkippedAccount, 0, len(errs))
	for _, err := range errs {
		var skip *Skip
		if errors.As(err, &skip) {
			out = append(out, protocol.SkippedAccount{AccountMemberID: string(skip.Account), Error: skip.Reason})
			continue
		}
		out = append(out, protocol.SkippedAccount{Error: ReasonList})
	}
	return out
}

func candidates(ctx context.Context, st store.Store, launcher domain.MemberID, policy domain.AccountFallback) ([]domain.MemberID, error) {
	if len(policy.Order) > 0 {
		return policy.Order, nil
	}
	owners, err := st.ListAccountOwners(ctx, launcher)
	if err != nil {
		return nil, err
	}
	out := []domain.MemberID{launcher}
	for _, owner := range owners {
		out = append(out, owner.ID)
	}
	return out, nil
}

// usedPercent reports the highest used share of account's quota windows in
// force for harness. ok is false when that usage cannot be read: an agent
// without readable quota, a missing or API-key login, a failed or stale read,
// or a row reporting none of those windows.
func usedPercent(ctx context.Context, usage QuotaReader, account domain.MemberID, harness string) (used float64, ok bool) {
	ids := windows[harness]
	if ids == nil {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	providers, err := usage.Read(ctx, account, false)
	if err != nil {
		return 0, false
	}
	now := time.Now()
	for _, provider := range providers {
		if provider.Provider != harness {
			continue
		}
		if provider.Status != "ok" {
			return 0, false
		}
		reported := false
		for _, window := range provider.Windows {
			if !slices.Contains(ids, window.ID) {
				continue
			}
			reported = true
			if window.ResetsAt == nil || window.ResetsAt.After(now) {
				used = max(used, window.UsedPercent)
			}
		}
		return used, reported
	}
	return 0, false
}
