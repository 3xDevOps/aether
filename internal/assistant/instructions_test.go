package assistant

import (
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/mcpbridge"
)

// bridgeTools lists the tools the assistant's MCP bridge serves.
func bridgeTools(t *testing.T) []string {
	t.Helper()
	toBridge, fromClient := io.Pipe()
	toClient, fromBridge := io.Pipe()
	go func() {
		_ = mcpbridge.Run(t.Context(), mcpbridge.Config{Socket: "/nonexistent", Assistant: true, In: toBridge, Out: fromBridge})
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: toClient, Writer: fromClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	var names []string
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	return names
}

func TestInstructionsNameOnlyToolsThatExistOrAreMarkedOptional(t *testing.T) {
	const optionalHeading = "### Tools that may exist"
	start := strings.Index(Instructions, optionalHeading)
	if start < 0 {
		t.Fatalf("instructions lack %q", optionalHeading)
	}
	section := Instructions[start+len(optionalHeading):]
	if end := strings.Index(section, "\n#"); end >= 0 {
		section = section[:end]
	}
	toolName := regexp.MustCompile("`(aether_[a-z_]+\\*?)`")
	var optional []string
	for _, m := range toolName.FindAllStringSubmatch(section, -1) {
		optional = append(optional, m[1])
	}
	if len(optional) == 0 {
		t.Fatal("the optional tool list is empty")
	}
	known := func(name string) bool {
		return slices.ContainsFunc(optional, func(o string) bool {
			prefix, wild := strings.CutSuffix(o, "*")
			return name == o || wild && strings.HasPrefix(name, prefix)
		})
	}

	served := bridgeTools(t)
	mentioned := map[string]bool{}
	for _, m := range toolName.FindAllStringSubmatch(Instructions, -1) {
		mentioned[m[1]] = true
		if !slices.Contains(served, m[1]) && !known(m[1]) {
			t.Errorf("instructions name %s, which the bridge does not serve and the optional list does not mark", m[1])
		}
	}
	for _, name := range served {
		if !mentioned[name] {
			t.Errorf("the bridge serves %s, which the instructions never name", name)
		}
	}
}
