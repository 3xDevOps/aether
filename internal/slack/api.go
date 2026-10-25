package slack

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const API = "https://slack.com/api/"

const (
	callTimeout  = 15 * time.Second
	maxReplySize = 1 << 20
)

type api struct {
	base   string
	client *http.Client
}

// identity is the bot as auth.test reports it.
type identity struct {
	UserID string `json:"user_id"`
	User   string `json:"user"`
	TeamID string `json:"team_id"`
	Team   string `json:"team"`
}

type block = map[string]any

type message struct {
	Channel     string  `json:"channel"`
	TS          string  `json:"ts,omitempty"`
	ThreadTS    string  `json:"thread_ts,omitempty"`
	Text        string  `json:"text"`
	Blocks      []block `json:"blocks,omitempty"`
	UnfurlLinks bool    `json:"unfurl_links"`
	UnfurlMedia bool    `json:"unfurl_media"`
}

// call posts params, or nothing when nil, to method as token and decodes
// the answer into out. Slack reports its own errors as ok: false.
func (a api) call(ctx context.Context, token, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var body io.Reader
	contentType := "application/x-www-form-urlencoded"
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("slack: %s: %w", method, err)
		}
		body, contentType = bytes.NewReader(raw), "application/json; charset=utf-8"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+method, body)
	if err != nil {
		return fmt.Errorf("slack: %s: %w", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplySize))
	if err != nil {
		return fmt.Errorf("slack: %s: read answer: %w", method, err)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("slack: %s: rate limited, retry after %ss", method, resp.Header.Get("Retry-After"))
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("slack: %s: %s: %s", method, resp.Status, strings.TrimSpace(string(raw)))
	}
	var head struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return fmt.Errorf("slack: %s: decode answer: %w", method, err)
	}
	if !head.OK {
		return &apiError{method: method, code: cmp.Or(head.Error, "not ok")}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("slack: %s: decode answer: %w", method, err)
	}
	return nil
}

type apiError struct{ method, code string }

func (e *apiError) Error() string { return "slack: " + e.method + ": " + e.code }

// lost reports whether Slack refused to edit a message for good: it no
// longer has it, or another app posted it.
func lost(err error) bool {
	var e *apiError
	if !errors.As(err, &e) {
		return false
	}
	switch e.code {
	case "message_not_found", "cant_update_message", "channel_not_found", "is_archived", "edit_window_closed":
		return true
	}
	return false
}

func (s *Service) post(ctx context.Context, m message) (string, error) {
	var out struct {
		TS string `json:"ts"`
	}
	err := s.api.call(ctx, s.cfg.BotToken, "chat.postMessage", m, &out)
	return out.TS, err
}

func (s *Service) update(ctx context.Context, m message) error {
	return s.api.call(ctx, s.cfg.BotToken, "chat.update", m, nil)
}

func (s *Service) reply(ctx context.Context, channel, ts, text string) {
	_, err := s.post(ctx, message{Channel: channel, ThreadTS: ts, Text: escape(text)})
	logged("reply", err, "channel", channel)
}

// escape keeps text from mentioning anyone or linking anywhere: Slack
// reads only &, < and > as markup's start.
func escape(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}
