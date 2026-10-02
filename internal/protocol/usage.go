package protocol

// MethodAccountUsage reports the selected account's read-only vendor quota.
const MethodAccountUsage = "account.usage"

// AccountUsageParams selects the account whose vendor quota is read. An empty
// account_member_id means the authenticated member's own account. Refresh
// bypasses the normal successful-result cache when the server permits it.
type AccountUsageParams struct {
	AccountMemberID string `json:"account_member_id,omitempty"`
	Refresh         bool   `json:"refresh,omitempty"`
}

// AccountUsageResult is the result of account.usage. Providers is always a
// JSON array, including when no provider row is available.
type AccountUsageResult struct {
	AccountMemberID string          `json:"account_member_id"`
	Providers       []UsageProvider `json:"providers"`
}

// UsageProvider is one vendor quota row. UpdatedAt and RetryAt are omitted
// when the provider has no corresponding observation or retry deadline.
type UsageProvider struct {
	Provider  string        `json:"provider"`
	Status    string        `json:"status"`
	Windows   []UsageWindow `json:"windows"`
	Plan      string        `json:"plan,omitempty"`
	UpdatedAt *string       `json:"updated_at,omitempty"`
	CheckedAt string        `json:"checked_at"`
	RetryAt   *string       `json:"retry_at,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// UsageWindow is one provider-reported quota window. A missing reset time is
// represented by an omitted resets_at field; it is never synthesized.
type UsageWindow struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    *string `json:"resets_at,omitempty"`
}
