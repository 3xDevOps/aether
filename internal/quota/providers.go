package quota

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type claudeUsageShape struct {
	FiveHour       *claudeWindowShape `json:"five_hour"`
	SevenDay       *claudeWindowShape `json:"seven_day"`
	SevenDaySonnet *claudeWindowShape `json:"seven_day_sonnet"`
	SevenDayOpus   *claudeWindowShape `json:"seven_day_opus"`
}

type claudeWindowShape struct {
	Utilization    json.RawMessage `json:"utilization"`
	UsedPercentage json.RawMessage `json:"used_percentage"`
	ResetsAt       json.RawMessage `json:"resets_at"`
}

type codexUsageShape struct {
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		PrimaryWindow   *codexWindowShape `json:"primary_window"`
		SecondaryWindow *codexWindowShape `json:"secondary_window"`
	} `json:"rate_limit"`
}

type codexWindowShape struct {
	UsedPercent        json.RawMessage `json:"used_percent"`
	LimitWindowSeconds json.RawMessage `json:"limit_window_seconds"`
	ResetAt            json.RawMessage `json:"reset_at"`
}

func fetchClaude(ctx context.Context, token string) (usageResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageEndpoint, nil)
	if err != nil {
		return usageResult{}, errors.New("could not create Claude usage request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-code/2.1.0")
	resp, err := usageHTTPClient.Do(req)
	if err != nil {
		return usageResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = readResponse(resp)
		return usageResult{}, safeHTTPError(resp.StatusCode, resp.Header, time.Now().UTC())
	}
	body, err := readResponse(resp)
	if err != nil {
		return usageResult{}, &responseError{reason: "Claude usage response could not be read"}
	}
	var shape claudeUsageShape
	if err := parseJSON(body, &shape); err != nil {
		return usageResult{}, &responseError{reason: "Claude usage response is malformed"}
	}
	windows := make([]Window, 0, 4)
	for _, item := range []struct {
		id     string
		label  string
		window *claudeWindowShape
	}{
		{"five_hour", "5 hours", shape.FiveHour},
		{"seven_day", "7 days", shape.SevenDay},
		{"seven_day_sonnet", "7 days (Sonnet)", shape.SevenDaySonnet},
		{"seven_day_opus", "7 days (Opus)", shape.SevenDayOpus},
	} {
		id, label, window := item.id, item.label, item.window
		if window == nil {
			continue
		}
		used, ok := parsePercentage(window.Utilization)
		if !ok {
			used, ok = parsePercentage(window.UsedPercentage)
		}
		if !ok {
			continue
		}
		reset, hasReset := parseTime(window.ResetsAt)
		var resetPtr *time.Time
		if hasReset {
			resetPtr = &reset
		}
		windows = append(windows, Window{ID: id, Label: label, UsedPercent: used, ResetsAt: resetPtr})
	}
	if len(windows) == 0 {
		return usageResult{}, &responseError{reason: "Claude usage response contained no valid windows"}
	}
	return usageResult{windows: windows}, nil
}

func fetchCodex(ctx context.Context, token, accountID string) (usageResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexUsageEndpoint, nil)
	if err != nil {
		return usageResult{}, errors.New("could not create Codex usage request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ChatGPT-Account-Id", accountID)
	req.Header.Set("User-Agent", "codex-cli")
	req.Header.Set("OpenAI-Beta", "codex-1")
	req.Header.Set("originator", "Codex Desktop")
	resp, err := usageHTTPClient.Do(req)
	if err != nil {
		return usageResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = readResponse(resp)
		return usageResult{}, safeHTTPError(resp.StatusCode, resp.Header, time.Now().UTC())
	}
	body, err := readResponse(resp)
	if err != nil {
		return usageResult{}, &responseError{reason: "Codex usage response could not be read"}
	}
	var shape codexUsageShape
	if err := parseJSON(body, &shape); err != nil {
		return usageResult{}, &responseError{reason: "Codex usage response is malformed"}
	}
	if shape.RateLimit == nil {
		return usageResult{}, &responseError{reason: "Codex usage response contained no rate limits"}
	}
	windows := make([]Window, 0, 2)
	for _, item := range []struct {
		id     string
		label  string
		window *codexWindowShape
	}{
		{"primary", "Primary", shape.RateLimit.PrimaryWindow},
		{"secondary", "Secondary", shape.RateLimit.SecondaryWindow},
	} {
		id, label, window := item.id, item.label, item.window
		if window == nil {
			continue
		}
		used, ok := parsePercentage(window.UsedPercent)
		if !ok {
			continue
		}
		reset, hasReset := parseTime(window.ResetAt)
		var resetPtr *time.Time
		if hasReset {
			resetPtr = &reset
		}
		windows = append(windows, Window{ID: id, Label: label, UsedPercent: used, ResetsAt: resetPtr})
	}
	if len(windows) == 0 {
		return usageResult{}, &responseError{reason: "Codex usage response contained no valid windows"}
	}
	return usageResult{windows: windows, plan: shape.PlanType}, nil
}

func parsePercentage(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || rawIsNull(raw) {
		return 0, false
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err != nil || number < 0 || number > 100 {
		return 0, false
	}
	return number, true
}

func parseTime(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 || rawIsNull(raw) {
		return time.Time{}, false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && strings.TrimSpace(text) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, text)
		if err == nil {
			return parsed.UTC(), true
		}
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil && number > 0 {
		return time.Unix(int64(number), 0).UTC(), true
	}
	return time.Time{}, false
}
