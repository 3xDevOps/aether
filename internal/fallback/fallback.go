// Package fallback decides which agent account a launch runs on: the account
// the launcher asked for, or, when that account's vendor usage is exhausted,
// another account the launcher may launch on.
package fallback

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/quota"
	"github.com/3xDevOps/Aether/internal/store"
)

// ExhaustedPercent is the used share of a quota window at which an account
// counts as exhausted: at 95% one long agent turn can spend the rest within
// minutes.
const ExhaustedPercent = 95

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

// Policy turns a launch request's fallback parameters into a policy. An
// order lists the accounts to try; disabled turns fallback off.
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
	candidates, err := candidates(ctx, st, launcher, policy)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate == account {
			continue
		}
		if _, err := ResolveLaunchAccount(ctx, st, launcher, string(candidate)); err != nil {
			continue
		}
		if used, ok := usedPercent(ctx, usage, candidate, harness); ok {
			measured[candidate] = used
		}
	}
	return measured, nil
}

// Select returns the account launcher's run launches on, from the usage
// Measure read. The caller has admitted launcher's launch on requested and
// holds the shared authorization mutex until the scheduler's durable launch
// boundary, so each candidate needs only its account authorization checked
// here; an account Measure could not read is never chosen. When no candidate
// qualifies, the launch stays on the requested account.
func Select(ctx context.Context, st store.Store, measured Measured, launcher, requested domain.MemberID, policy domain.AccountFallback) (domain.MemberID, error) {
	if policy.Disabled {
		return requested, nil
	}
	if used, ok := measured[requested]; !ok || used < ExhaustedPercent {
		return requested, nil
	}
	candidates, err := candidates(ctx, st, launcher, policy)
	if err != nil {
		return "", err
	}
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
			if errors.Is(authErr, permissions.ErrDenied) || errors.Is(authErr, store.ErrNotFound) {
				continue
			}
			return "", authErr
		}
		used, ok := measured[candidate]
		if !ok || used >= ExhaustedPercent {
			continue
		}
		if len(policy.Order) > 0 {
			return candidate, nil
		}
		found = append(found, usable{candidate, used})
	}
	if len(found) == 0 {
		return requested, nil
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].used != found[j].used {
			return found[i].used < found[j].used
		}
		return found[i].account < found[j].account
	})
	return found[0].account, nil
}

// candidates is the policy's order, or else launcher's own account and every
// account shared with launcher.
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
// without readable quota, a missing or API-key login, or a failed or stale
// read.
func usedPercent(ctx context.Context, usage QuotaReader, account domain.MemberID, harness string) (used float64, ok bool) {
	ids := windows[harness]
	if ids == nil {
		return 0, false
	}
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
		for _, window := range provider.Windows {
			if slices.Contains(ids, window.ID) && (window.ResetsAt == nil || window.ResetsAt.After(now)) {
				used = max(used, window.UsedPercent)
			}
		}
		return used, true
	}
	return 0, false
}
