package github

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	errorBodyLimit   = 64 * 1024
	errorMessageMax  = 300
	secondaryBackoff = time.Minute
)

var (
	// ErrUnauthorized matches an APIError for HTTP 401: the token was rejected.
	ErrUnauthorized = errors.New("github: credentials rejected")
	// ErrNotFound matches an APIError for HTTP 404: the resource does not
	// exist or the token cannot see it.
	ErrNotFound = errors.New("github: not found")
	// ErrMoved matches an APIError for HTTP 301: the repository was renamed
	// or transferred, and retrying the old owner/name never succeeds.
	ErrMoved = errors.New("github: repository moved")
)

// APIError is a non-200 GitHub response. Message is GitHub's own error text,
// bounded and with the request token redacted.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return "github: API request: " + statusLine(e.Status, e.Message)
}

func (e *APIError) Is(target error) bool {
	return target == ErrUnauthorized && e.Status == http.StatusUnauthorized ||
		target == ErrNotFound && e.Status == http.StatusNotFound ||
		target == ErrMoved && e.Status == http.StatusMovedPermanently
}

// RateLimitError is a primary or secondary GitHub rate limit. RetryAt comes
// from Retry-After, then X-RateLimit-Reset when X-RateLimit-Remaining is 0;
// otherwise it is one minute, the least GitHub asks for.
type RateLimitError struct {
	Status  int
	Message string
	RetryAt time.Time
}

func (e *RateLimitError) Error() string {
	return "github: API rate limit: " + statusLine(e.Status, e.Message) + "; retry after " + e.RetryAt.UTC().Format(time.RFC3339)
}

// ReviewsError is a snapshot read that failed at the reviews, after the pull
// request and its checks were read; the snapshot returned with it holds
// those.
type ReviewsError struct{ Err error }

func (e *ReviewsError) Error() string { return "read reviews: " + e.Err.Error() }
func (e *ReviewsError) Unwrap() error { return e.Err }

func statusLine(status int, message string) string {
	line := "HTTP " + strconv.Itoa(status) + " " + http.StatusText(status)
	if message != "" && message != http.StatusText(status) {
		line += ": " + message
	}
	return line
}

func responseError(response *http.Response, token string, now time.Time) error {
	message := ""
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		message = errorMessage(response.Body, token)
	}
	if response.StatusCode == http.StatusMovedPermanently {
		message = "the repository was renamed or transferred; watch it under its new owner/name"
	}
	header := response.Header
	limited := response.StatusCode == http.StatusTooManyRequests ||
		response.StatusCode == http.StatusForbidden && (header.Get("X-RateLimit-Remaining") == "0" ||
			header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(message), "rate limit"))
	if !limited {
		return &APIError{Status: response.StatusCode, Message: message}
	}
	return &RateLimitError{Status: response.StatusCode, Message: message, RetryAt: retryAt(header, now)}
}

func retryAt(header http.Header, now time.Time) time.Time {
	if value := header.Get("Retry-After"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return now.Add(time.Duration(seconds) * time.Second)
		}
		if at, err := http.ParseTime(value); err == nil {
			return at
		}
	}
	if header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > 0 {
			return time.Unix(reset, 0)
		}
	}
	return now.Add(secondaryBackoff)
}

func errorMessage(body io.Reader, token string) string {
	var record struct {
		Message string `json:"message"`
	}
	data, err := io.ReadAll(io.LimitReader(body, errorBodyLimit))
	if err != nil || json.Unmarshal(data, &record) != nil {
		return ""
	}
	message := record.Message
	if token != "" {
		message = strings.ReplaceAll(message, token, "[redacted]")
	}
	message = strings.Join(strings.FieldsFunc(message, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) }), " ")
	return truncate(message, errorMessageMax)
}

func truncate(text string, limit int) string {
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}
