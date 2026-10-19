package mcpbridge

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	toolMemorySearch = "aether_memory_search"
	toolMemoryAdd    = "aether_memory_add"
)

type memorySearchInput struct {
	Query string `json:"query,omitempty" jsonschema:"words matched against note titles, bodies and cited files, best first; omit for the newest notes"`
	ID    string `json:"id,omitempty" jsonschema:"a note id: return that note with the notes it replaced or was replaced by; the other fields are then ignored"`
	Kind  string `json:"kind,omitempty" jsonschema:"only notes of this kind"`
	File  string `json:"file,omitempty" jsonschema:"only notes citing this repository path"`
	All   bool   `json:"all,omitempty" jsonschema:"include replaced and invalidated notes"`
}

type memorySearchOutput struct {
	Items []protocol.MemoryItem `json:"items"`
	Chain []protocol.MemoryItem `json:"chain,omitempty"`
}

type memoryAddInput struct {
	Kind       string   `json:"kind" jsonschema:"gotcha after solving a non-obvious failure, decision when choosing between approaches, convention or preference"`
	Text       string   `json:"text" jsonschema:"the note, at most 1024 bytes; its first line is its title"`
	Files      []string `json:"files,omitempty" jsonschema:"repository paths the note cites, at most 20"`
	Supersedes string   `json:"supersedes,omitempty" jsonschema:"id of an unpinned agent note this one replaces"`
	Scope      string   `json:"scope,omitempty" jsonschema:"member: a pin on this note enters only the pinning member's briefs; omit for workspace"`
}

func memorySchema[T any](tool string, enums map[string][]any) (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		return nil, fmt.Errorf("mcpbridge: %s schema: %w", tool, err)
	}
	for name, values := range enums {
		schema.Properties[name].Enum = values
	}
	return schema, nil
}

func registerMemoryTools(srv *mcp.Server, socket string) error {
	agentKinds := []any{protocol.MemoryKindDecision, protocol.MemoryKindConvention, protocol.MemoryKindGotcha, protocol.MemoryKindPreference}
	search, err := memorySchema[memorySearchInput](toolMemorySearch, map[string][]any{"kind": append(agentKinds, protocol.MemoryKindOutcome)})
	if err != nil {
		return err
	}
	add, err := memorySchema[memoryAddInput](toolMemoryAdd, map[string][]any{
		"kind": agentKinds, "scope": {protocol.MemoryScopeWorkspace, protocol.MemoryScopeMember},
	})
	if err != nil {
		return err
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name: toolMemorySearch,
		Description: "Search team memory: short notes that teammates and earlier runs in this workspace wrote, decisions, conventions, gotchas, preferences and run outcomes. " +
			"Search before debugging a failure or re-deciding a convention. Returns up to 10 notes, best first, each with its author, run, harness, commit, cited files and pull request; pass id for one note and its supersede chain. " +
			coordhooks.MemoryRule,
		InputSchema: search,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in memorySearchInput) (*mcp.CallToolResult, memorySearchOutput, error) {
		out := memorySearchOutput{Items: []protocol.MemoryItem{}}
		if in.ID != "" {
			var shown protocol.MemoryShowResult
			if err := coordtransport.Call(ctx, socket, protocol.MethodMemoryShow, protocol.MemoryShowParams{ID: in.ID}, &shown); err != nil {
				return failed(err), out, nil
			}
			out.Items, out.Chain = []protocol.MemoryItem{shown.Item}, shown.Chain
			return nil, out, nil
		}
		p := protocol.MemorySearchParams{Words: in.Query, Kind: in.Kind, File: in.File, All: in.All, Limit: protocol.MemorySearchLimit}
		var found protocol.MemorySearchResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodMemorySearch, p, &found); err != nil {
			return failed(err), out, nil
		}
		if found.Items != nil {
			out.Items = found.Items
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: toolMemoryAdd,
		Description: "Record a team memory note that every run in this workspace can search: a gotcha after solving a non-obvious failure, or a decision when choosing between approaches, citing the files it concerns. " +
			"The server records your member, run, harness and commit, and refuses text holding a secret. Adding the same text again returns the existing note; the result lists up to 3 similar notes. " +
			"A note reaches new runs only after a member pins it (aether memory pin <id>); agents cannot pin or remove notes. " +
			coordhooks.MemoryRule,
		InputSchema: add,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in memoryAddInput) (*mcp.CallToolResult, protocol.MemoryAddResult, error) {
		p := protocol.MemoryAddParams{Kind: in.Kind, Text: in.Text, Files: in.Files, Supersedes: in.Supersedes, Scope: in.Scope}
		var out protocol.MemoryAddResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodMemoryAdd, p, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})
	return nil
}
