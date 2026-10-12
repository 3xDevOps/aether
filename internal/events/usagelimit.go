package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeRunUsageLimit reports that a run's agent stopped because its account
// hit a subscription usage limit.
const TypeRunUsageLimit Type = "run.usage_limit"

// RunUsageLimitPayload carries the agent's own usage-limit message.
type RunUsageLimitPayload struct {
	Message string `json:"message"`
}

func (RunUsageLimitPayload) EventType() Type { return TypeRunUsageLimit }

func init() { registerPayload[RunUsageLimitPayload](TypeRunUsageLimit) }

// TypeRunAccount reports that a live run moved to another agent account.
const TypeRunAccount Type = "run.account"

// RunAccountPayload is a run's move from Previous to Account. Requested is
// the account the run first asked for, empty when it is Account; the run
// snapshot carries the same fields.
type RunAccountPayload struct {
	Account   domain.MemberID `json:"account_member_id"`
	Previous  domain.MemberID `json:"previous_account_member_id"`
	Requested domain.MemberID `json:"requested_account_member_id,omitempty"`
}

func (RunAccountPayload) EventType() Type { return TypeRunAccount }

func init() { registerPayload[RunAccountPayload](TypeRunAccount) }
