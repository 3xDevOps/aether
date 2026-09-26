package runrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// Projection occurs inside real gh, before Runtime.Exec captures output. Each
// page is small even for maximum-sized GitHub bodies; clipped fields are never
// passed off as complete review evidence.
const feedbackProjection = `map({id:(.id|tostring),review_id:((.pull_request_review_id // "")|tostring),author:(.user.login // ""),body:((.body // "")[:1024]),url:(.html_url // ""),created_at:(.created_at // ""),submitted_at:(.submitted_at // ""),state:(.state // ""),path:((.path // "")[:1024]),line,original_line,side,start_line,start_side,commit_oid:(.commit_id // ""),in_reply_to_id:((.in_reply_to_id // "")|tostring),diff_hunk:((.diff_hunk // "")[:512]),truncated:(((.body // "")|length)>1024 or ((.diff_hunk // "")|length)>512 or ((.path // "")|length)>1024)})`
const checkProjection = `.check_runs | map({name,status,conclusion,url:.html_url})`
const statusProjection = `map({name:.context,status:(if .state == "pending" then "IN_PROGRESS" else "COMPLETED" end),conclusion:(if .state == "pending" then "" else .state end),url:.target_url})`

// feedbackPages visits bounded REST pages without asking gh to aggregate an
// unbounded history. A one-page lookahead distinguishes exactly-full responses
// from omitted items. budget is shared across all four feedback categories.
func (s *Service) feedbackPages(ctx context.Context, run Execution, endpoint, projection string, limit int, budget *int, consume func(json.RawMessage) error) (bool, CommandOutput, error) {
	const pageSize = 8
	count := 0
	truncated := false
	var diagnostic CommandOutput
	for page := 1; ; page++ {
		path := endpoint + "?per_page=" + strconv.Itoa(pageSize) + "&page=" + strconv.Itoa(page)
		out, err := s.gh(ctx, run, false, path, "--jq", projection)
		diagnostic = boundOutput(CommandOutput{Stderr: diagnostic.Stderr + out.Stderr, Truncated: diagnostic.Truncated || out.Truncated, ExitCode: out.ExitCode})
		text, err := complete(out, err)
		if err != nil {
			return truncated || out.Truncated, FailureOutput(out, err), err
		}
		var items []json.RawMessage
		if err := json.Unmarshal([]byte(text), &items); err != nil {
			return truncated, out, fmt.Errorf("runrepo: decode GitHub feedback page: %w", err)
		}
		for _, item := range items {
			if count == limit || len(item) > *budget {
				return true, diagnostic, nil
			}
			var marker struct {
				Truncated bool `json:"truncated"`
			}
			if err := json.Unmarshal(item, &marker); err != nil {
				return truncated, out, fmt.Errorf("runrepo: decode GitHub feedback bounds: %w", err)
			}
			if err := consume(item); err != nil {
				return truncated, out, fmt.Errorf("runrepo: decode GitHub feedback item: %w", err)
			}
			truncated = truncated || marker.Truncated
			*budget -= len(item)
			count++
		}
		if len(items) < pageSize {
			return truncated, diagnostic, nil
		}
	}
}

// PRFeedback discovers the exact PR first, including PRs created directly by
// gh. Every page rechecks account authority. Earlier categories survive a later
// failure, with real diagnostics and explicit incompleteness.
func (s *Service) PRFeedback(ctx context.Context, run Execution, target PRTarget, limit int) (PRFeedback, error) {
	result := PRFeedback{Checks: []protocol.RunPRCheck{}, Comments: []protocol.RunPRComment{}, Reviews: []protocol.RunPRReview{}, ReviewComments: []protocol.RunPRReviewComment{}}
	if limit < 0 || limit > protocol.MaxRunPRFeedbackEntries {
		return result, errors.New("runrepo: feedback limit must be between 1 and 100 (or zero for the default)")
	}
	if limit == 0 {
		limit = protocol.MaxRunPRFeedbackEntries
	}
	status, err := s.LookupPR(ctx, run, target)
	result.Identity, result.PullRequest = status.Identity, status.PullRequest
	if err != nil {
		result.Output = FailureOutput(status.Output, err)
		return result, err
	}
	if status.PullRequest == nil {
		return result, nil
	}
	pr := status.PullRequest
	if !validOID(pr.Head) {
		return result, errors.New("runrepo: GitHub PR response omitted full head object ID")
	}
	prefix := "repos/" + target.Repository
	pullPath := prefix + "/pulls/" + strconv.Itoa(pr.Number)
	budget := protocol.MaxRunRepoOutputBytes
	seenStatus := make(map[string]bool)
	for index, collection := range []struct {
		path       string
		projection string
		consume    func(json.RawMessage) error
	}{
		{prefix + "/commits/" + pr.Head + "/check-runs", checkProjection, func(raw json.RawMessage) error {
			var item protocol.RunPRCheck
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			result.Checks = append(result.Checks, item)
			return nil
		}},
		{prefix + "/commits/" + pr.Head + "/statuses", statusProjection, func(raw json.RawMessage) error {
			var item protocol.RunPRCheck
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			// The statuses endpoint is newest first. Only the latest state of
			// a legacy context is current, unlike its historical transitions.
			if seenStatus[item.Name] {
				return nil
			}
			seenStatus[item.Name] = true
			result.Checks = append(result.Checks, item)
			return nil
		}},
		{prefix + "/issues/" + strconv.Itoa(pr.Number) + "/comments", feedbackProjection, func(raw json.RawMessage) error {
			var item protocol.RunPRComment
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			result.Comments = append(result.Comments, item)
			return nil
		}},
		{pullPath + "/reviews", feedbackProjection, func(raw json.RawMessage) error {
			var item protocol.RunPRReview
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			result.Reviews = append(result.Reviews, item)
			return nil
		}},
		{pullPath + "/comments", feedbackProjection, func(raw json.RawMessage) error {
			var item protocol.RunPRReviewComment
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			result.ReviewComments = append(result.ReviewComments, item)
			return nil
		}},
	} {
		if budget == 0 {
			result.Truncated = true
			break
		}
		collectionLimit := limit
		if index == 1 {
			collectionLimit -= len(result.Checks)
		}
		truncated, out, err := s.feedbackPages(ctx, run, collection.path, collection.projection, collectionLimit, &budget, collection.consume)
		result.Truncated = result.Truncated || truncated
		result.Output = boundOutput(CommandOutput{Stderr: result.Output.Stderr + out.Stderr, Truncated: result.Output.Truncated || out.Truncated, ExitCode: out.ExitCode})
		if err != nil {
			result.Truncated = true
			result.Output = FailureOutput(out, err)
			return result, err
		}
	}
	return result, nil
}
