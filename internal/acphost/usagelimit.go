package acphost

import (
	"encoding/json"
	"errors"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/agentstatus"
)

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
	case data.ErrorKind == "rate_limit" && agentstatus.IsUsageLimitMessage(text):
		return text, true
	case data.CodexErrorInfo == "usageLimitExceeded":
		return data.Message, true
	case data.ErrorName == "provider.quota":
		return text, true
	}
	return "", false
}
