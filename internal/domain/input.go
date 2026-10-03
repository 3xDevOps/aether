package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RunInputRequest identifies a pending native interaction without exposing its
// prompt, answers, paths, or transcript. Identity includes session and kind.
type RunInputRequest struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
}

// RunInputUpdate changes only input state, never execution state. Clear closes
// one terminated session; replace is the adapter's complete authoritative set.
type RunInputUpdate struct {
	Operation string            `json:"operation"`
	SessionID string            `json:"session_id,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	ID        string            `json:"id,omitempty"`
	Requests  []RunInputRequest `json:"requests,omitempty"`
}

const (
	MaxRunInputRequests = 128
	MaxRunInputIDBytes  = 256
)

// ValidateRunInputUpdates bounds semi-trusted reporter metadata before it reaches
// persistence. Identifiers are opaque: reject rather than truncate or normalize
// them, since either could make a close resolve an unrelated request.
func ValidateRunInputUpdates(updates []RunInputUpdate) error {
	if len(updates) > MaxRunInputRequests {
		return fmt.Errorf("input_updates exceeds %d updates", MaxRunInputRequests)
	}
	for _, update := range updates {
		switch update.Operation {
		case "open", "close":
			if err := validateRunInputRequest(RunInputRequest{ID: update.ID, SessionID: update.SessionID, Kind: update.Kind}); err != nil {
				return err
			}
			if len(update.Requests) != 0 {
				return fmt.Errorf("%s input update cannot include requests", update.Operation)
			}
		case "clear":
			if !validRunInputID(update.SessionID) || update.ID != "" || update.Kind != "" || len(update.Requests) != 0 {
				return fmt.Errorf("clear input update requires only a valid session_id")
			}
		case "replace":
			if update.SessionID != "" || update.ID != "" || update.Kind != "" {
				return fmt.Errorf("replace input update requires only requests")
			}
			if len(update.Requests) > MaxRunInputRequests {
				return fmt.Errorf("input requests exceeds %d requests", MaxRunInputRequests)
			}
			for _, request := range update.Requests {
				if err := validateRunInputRequest(request); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unknown input operation %q", update.Operation)
		}
	}
	return nil
}

func validateRunInputRequest(request RunInputRequest) error {
	if !validRunInputID(request.ID) || !validRunInputID(request.SessionID) {
		return fmt.Errorf("input request requires valid id and session_id (1-%d bytes, no control characters)", MaxRunInputIDBytes)
	}
	switch request.Kind {
	case "question", "permission", "form", "extension_ui":
		return nil
	default:
		return fmt.Errorf("unknown input kind %q", request.Kind)
	}
}

func validRunInputID(id string) bool {
	if len(id) == 0 || len(id) > MaxRunInputIDBytes || !utf8.ValidString(id) || strings.TrimSpace(id) == "" {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
