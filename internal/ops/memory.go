package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type memoryAddInput struct {
	WorkspaceID string   `json:"workspace_id"`
	Kind        string   `json:"kind"`
	Text        string   `json:"text" jsonschema:"one fact, with why"`
	Files       []string `json:"files,omitempty" jsonschema:"repo paths it cites"`
	Supersedes  string   `json:"supersedes,omitempty" jsonschema:"note id it replaces"`
}

type memoryPinInput struct {
	WorkspaceID string `json:"workspace_id"`
	ID          string `json:"id"`
	Pin         bool   `json:"pin"`
}

var memoryOps = []Op{
	opOf("aether_memory_add", "Add team memory", ScopeWrite, false, nextWorkspaces,
		"Use this when the user wants a decision, convention, gotcha or preference remembered for the workspace; a note with a secret is refused.", memoryAdd).
		enum("kind", protocol.MemoryKindDecision, protocol.MemoryKindConvention, protocol.MemoryKindGotcha, protocol.MemoryKindPreference).
		hints(false, false).offer(TierConfirmed).calling(protocol.MethodServerInfo, protocol.MethodMemoryAdd, protocol.MethodMemoryShow),
	opOf("aether_memory_pin", "Pin team memory", ScopeWrite, true, "call aether_memory_search for note ids",
		"Use this when the user pins a note into every new run's brief in the workspace, or unpins it.", memoryPin).
		hints(false, false).offer(TierConfirmed).calling(protocol.MethodServerInfo, protocol.MethodMemoryPin, protocol.MethodMemoryUnpin),
}

type note struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Files        []string `json:"files,omitempty"`
	Author       string   `json:"author,omitempty"`
	Source       string   `json:"source,omitempty"`
	SourceRef    string   `json:"source_ref,omitempty"`
	RunID        string   `json:"run_id,omitempty"`
	Harness      string   `json:"harness,omitempty"`
	Commit       string   `json:"commit,omitempty"`
	PRURL        string   `json:"pr_url,omitempty"`
	SupersededBy string   `json:"superseded_by,omitempty"`
	Pinned       bool     `json:"pinned"`
	CreatedAt    string   `json:"created_at"`
}

func noteOf(m protocol.MemoryItem, body int) note {
	return note{ID: m.ID, Kind: m.Kind, Title: clip(m.Title, maxReport), Body: clip(m.Body, body), Files: m.Files,
		Author: m.AuthorName, Source: m.Source, SourceRef: m.SourceRef, RunID: m.RunID, Harness: m.Harness, Commit: m.Commit, PRURL: m.PRURL,
		SupersededBy: m.SupersededBy, Pinned: m.PinnedAt != nil, CreatedAt: m.CreatedAt}
}

func memoryLink(ctx context.Context, read Call) (string, error) {
	l, err := dashboardLinks(ctx, read)
	if err != nil || l == "" {
		return "", err
	}
	return string(l) + "/?page=memory", nil
}

func memoryAdd(ctx context.Context, c calls, in memoryAddInput) (string, any, error) {
	url, err := memoryLink(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var res protocol.MemoryAddResult
	if err := c.act(ctx, protocol.MethodMemoryAdd, protocol.MemoryAddParams{WorkspaceID: in.WorkspaceID, Kind: in.Kind, Text: in.Text, Files: in.Files, Supersedes: in.Supersedes}, &res); err != nil {
		// The server refuses to replace a note twice, so a repeated
		// replacement returns the note that already replaced it.
		var perr *protocol.Error
		if in.Supersedes == "" || !errors.As(err, &perr) || perr.Code != protocol.CodeConflict {
			return "", nil, err
		}
		var old, next protocol.MemoryShowResult
		if c.read(ctx, protocol.MethodMemoryShow, protocol.MemoryShowParams{WorkspaceID: in.WorkspaceID, ID: in.Supersedes}, &old) != nil || old.Item.SupersededBy == "" ||
			c.read(ctx, protocol.MethodMemoryShow, protocol.MemoryShowParams{WorkspaceID: in.WorkspaceID, ID: old.Item.SupersededBy}, &next) != nil ||
			store.MemoryHash(next.Item.Body) != store.MemoryHash(in.Text) {
			return "", nil, err
		}
		res = protocol.MemoryAddResult{Item: next.Item, Duplicate: true}
	}
	similar := make([]string, 0, len(res.Similar))
	for _, m := range res.Similar {
		similar = append(similar, m.ID)
	}
	out := map[string]any{"id": res.Item.ID, "workspace_id": res.Item.WorkspaceID, "duplicate": res.Duplicate, "similar_ids": similar, "url": url}
	if res.Duplicate {
		return fmt.Sprintf("The workspace already remembers this as note %s; nothing new was added.", res.Item.ID), out, nil
	}
	return fmt.Sprintf("Added %s note %s.", res.Item.Kind, res.Item.ID), out, nil
}

func memoryPin(ctx context.Context, c calls, in memoryPinInput) (string, any, error) {
	url, err := memoryLink(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var item protocol.MemoryItem
	if in.Pin {
		var res protocol.MemoryPinResult
		err = c.act(ctx, protocol.MethodMemoryPin, protocol.MemoryPinParams{WorkspaceID: in.WorkspaceID, ID: in.ID, Scope: protocol.MemoryScopeWorkspace}, &res)
		item = res.Item
	} else {
		var res protocol.MemoryUnpinResult
		err = c.act(ctx, protocol.MethodMemoryUnpin, protocol.MemoryUnpinParams{WorkspaceID: in.WorkspaceID, ID: in.ID}, &res)
		item = res.Item
	}
	if err != nil {
		return "", nil, err
	}
	verb := "Unpinned"
	if item.PinnedAt != nil {
		verb = "Pinned"
	}
	return fmt.Sprintf("%s note %s.", verb, item.ID), map[string]any{"id": item.ID, "pinned": item.PinnedAt != nil, "url": url}, nil
}
