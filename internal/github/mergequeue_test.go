package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMergeQueueRemoval(t *testing.T) {
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	for _, tc := range []struct {
		name, body string
		reset      bool
		want       *QueueRemoval
		err        string
	}{
		{"failed checks", `{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[{"__typename":"RemovedFromMergeQueueEvent","createdAt":"2026-11-11T08:31:08Z","reason":"failed_checks"}]}}}}}`,
			false, &QueueRemoval{At: time.Date(2026, 11, 11, 8, 31, 8, 0, time.UTC), Reason: "failed_checks"}, ""},
		{"merged", `{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[{"__typename":"RemovedFromMergeQueueEvent","createdAt":"2026-11-11T09:08:02Z","reason":"merged"}]}}}}}`, false, nil, ""},
		{"queued", `{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[{"__typename":"AddedToMergeQueueEvent"}]}}}}}`, false, nil, ""},
		{"never queued", `{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[]}}}}}`, false, nil, ""},
		{"no repository", `{"data":{"repository":null}}`, false, nil, "GraphQL returned no pull request"},
		{"refused", `{"data":null,"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`, false, nil, "github: GraphQL: Resource not accessible by integration"},
		{"rate limited", `{"data":null,"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`, true, nil, "rate limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			s, _ := newTestService(t, nativeCredentials(), func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost || r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Fatalf("request = %s %s", r.Method, r.URL)
				}
				data, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(data, &sent); err != nil {
					t.Fatalf("body %q: %v", data, err)
				}
				response := jsonResponse(http.StatusOK, tc.body)
				if tc.reset {
					response.Header.Set("X-RateLimit-Remaining", "0")
					response.Header.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
				}
				return response, nil
			})
			got, err := s.MergeQueueRemoval(context.Background(), testToken, "acme/app", 7)
			if sent.Variables["owner"] != "acme" || sent.Variables["name"] != "app" || sent.Variables["number"] != float64(7) ||
				!strings.Contains(sent.Query, "REMOVED_FROM_MERGE_QUEUE_EVENT") {
				t.Fatalf("sent = %+v", sent)
			}
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				var limited *RateLimitError
				if tc.reset && (!errors.As(err, &limited) || !limited.RetryAt.Equal(reset)) {
					t.Fatalf("err = %#v, want a rate limit until %s", err, reset)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (tc.want == nil) || got != nil && (!got.At.Equal(tc.want.At) || got.Reason != tc.want.Reason) {
				t.Fatalf("removal = %+v, want %+v", got, tc.want)
			}
		})
	}
}
