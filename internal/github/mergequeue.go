package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// QueueRemoval is a merge queue removing a pull request without merging
// it. Reason is GitHub's, such as failed_checks or manual.
type QueueRemoval struct {
	At     time.Time
	Reason string
}

const lastQueueEventQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      timelineItems(itemTypes: [ADDED_TO_MERGE_QUEUE_EVENT, REMOVED_FROM_MERGE_QUEUE_EVENT], last: 1) {
        nodes { __typename ... on RemovedFromMergeQueueEvent { createdAt reason } }
      }
    }
  }
}`

// MergeQueueRemoval reads repo#number's latest merge queue event. It is
// nil while the pull request is queued, was never queued, or the queue
// last removed it by merging it. Only GraphQL says why the queue removed
// a pull request; the REST timeline records a removal before the merge
// that caused it.
func (s *Service) MergeQueueRemoval(ctx context.Context, token, repo string, number int) (*QueueRemoval, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || !validFullName(repo) || number <= 0 {
		return nil, errors.New("github: pull request must be an owner/name on github.com and a positive number")
	}
	query, err := json.Marshal(map[string]any{
		"query":     lastQueueEventQuery,
		"variables": map[string]any{"owner": owner, "name": name, "number": number},
	})
	if err != nil {
		return nil, err
	}
	var answer struct {
		Data struct {
			Repository *struct {
				PullRequest *struct {
					TimelineItems struct {
						Nodes []struct {
							Type      string    `json:"__typename"`
							CreatedAt time.Time `json:"createdAt"`
							Reason    string    `json:"reason"`
						} `json:"nodes"`
					} `json:"timelineItems"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, _, err = s.request(ctx, token, http.MethodPost, "/graphql", query, "", &answer); err != nil {
		return nil, err
	}
	if len(answer.Errors) > 0 {
		message := strings.ReplaceAll(answer.Errors[0].Message, token, "[redacted]")
		return nil, errors.New("github: GraphQL: " + truncate(message, errorMessageMax))
	}
	if answer.Data.Repository == nil || answer.Data.Repository.PullRequest == nil {
		return nil, errors.New("github: GraphQL returned no pull request")
	}
	nodes := answer.Data.Repository.PullRequest.TimelineItems.Nodes
	if len(nodes) != 1 || nodes[0].Type != "RemovedFromMergeQueueEvent" || nodes[0].Reason == "merged" {
		return nil, nil
	}
	return &QueueRemoval{At: nodes[0].CreatedAt, Reason: truncate(nodes[0].Reason, errorMessageMax)}, nil
}
