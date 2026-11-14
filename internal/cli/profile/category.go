package profile

import (
	"path"
	"strings"
)

// The categories the walk ranks files by, so the per-snapshot budget is
// spent on what a developer wrote before what a harness generated.
const (
	// CategoryMemory is the standing instructions an agent reads on every
	// run: CLAUDE.md, AGENTS.md, and anything under memory/.
	CategoryMemory = "memory"
	// CategorySkills is skills/.
	CategorySkills = "skills"
	// CategoryCommands is custom commands: commands/ and codex's prompts/.
	CategoryCommands = "commands"
	// CategorySettings is settings and config files.
	CategorySettings = "settings"
	// CategoryMCP is MCP server configuration.
	CategoryMCP = "mcp"
	// CategoryPlugins is plugins/.
	CategoryPlugins = "plugins"
	// CategoryOther is everything the rules above do not name.
	CategoryOther = "other"
)

// categoryOrder is the order categoryRank spends the budget in.
var categoryOrder = []string{
	CategoryMemory,
	CategorySkills,
	CategoryCommands,
	CategorySettings,
	CategoryMCP,
	CategoryPlugins,
	CategoryOther,
}

// memoryNames are the standing-instruction files agents read by name.
var memoryNames = map[string]bool{
	"claude.md": true,
	"agents.md": true,
	"agent.md":  true,
	"gemini.md": true,
	"memory.md": true,
}

// Classify names the category a profile-relative path belongs to. The
// first path segment decides for the directory-shaped categories; the
// basename decides for the loose files at a profile root.
func Classify(rel string) string {
	rel = path.Clean(rel)
	segments := strings.Split(rel, "/")
	if len(segments) > 1 {
		switch strings.ToLower(segments[0]) {
		case "skills":
			return CategorySkills
		case "commands", "prompts":
			return CategoryCommands
		case "plugins":
			return CategoryPlugins
		case "memory", "memories":
			return CategoryMemory
		case "mcp":
			return CategoryMCP
		}
	}
	base := strings.ToLower(path.Base(rel))
	switch {
	case memoryNames[base]:
		return CategoryMemory
	case strings.Contains(base, "mcp"):
		return CategoryMCP
	case strings.HasPrefix(base, "settings.") || strings.HasPrefix(base, "config."):
		return CategorySettings
	default:
		return CategoryOther
	}
}
