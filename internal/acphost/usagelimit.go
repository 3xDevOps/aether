package acphost

import (
	"encoding/json"
	"errors"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// usageLimitPrefixes is USAGE_LIMIT_ERROR_PREFIXES as exported by
// @anthropic-ai/claude-agent-sdk in sdk.d.ts: Claude Code starts every
// subscription usage-limit message with one of these.
var usageLimitPrefixes = [...]string{
	"You've hit your",
	"You've reached your",
	"You're out of usage credits",
	"Your org is out of usage · add funds to continue",
	"Your org is out of usage · contact your admin",
	"Your seat type doesn't include usage credits",
	"Your seat type doesn't include usage",
	"Your usage allocation has been disabled by your admin",
	"Your group's usage limit is set to $0",
	"Fable 5 requires usage credits",
	"You're out of extra usage",
	"Your seat type doesn't include extra usage",
}

// IsUsageLimitMessage reports whether text is Claude Code's wording for a
// subscription usage limit.
func IsUsageLimitMessage(text string) bool {
	for _, p := range usageLimitPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// UsageLimit reports whether err, as returned for a failed session/prompt,
// is the agent's subscription usage limit, and returns the agent's own
// message for it.
func UsageLimit(err error) (message string, ok bool) {
	var re *acp.RequestError
	if !errors.As(err, &re) || re.Data == nil {
		return "", false
	}
	raw, err := json.Marshal(re.Data)
	if err != nil {
		return "", false
	}
	var data struct {
		ErrorKind      string `json:"errorKind"`
		CodexErrorInfo any    `json:"codexErrorInfo"`
		Message        string `json:"message"`
		ErrorName      string `json:"errorName"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return "", false
	}
	text := strings.TrimPrefix(re.Message, "Internal error: ")
	switch {
	case data.ErrorKind == "rate_limit" && IsUsageLimitMessage(text):
		return text, true
	case data.CodexErrorInfo == "usageLimitExceeded":
		return data.Message, true
	case data.ErrorName == "provider.quota":
		return text, true
	}
	return "", false
}
