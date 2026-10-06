package main

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestPrintAgents(t *testing.T) {
	var b strings.Builder
	err := printAgents(&b, []protocol.AgentInfo{
		{Name: "claude", Source: "shipped"},
		{Name: "myagent", Source: "member"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "agent claude shipped\nagent myagent member\n"
	if b.String() != want {
		t.Fatalf("output = %q, want %q", b.String(), want)
	}
}

func TestPrintAgentsEmpty(t *testing.T) {
	var b strings.Builder
	if err := printAgents(&b, nil); err != nil {
		t.Fatal(err)
	}
	if b.String() != "no agents\n" {
		t.Fatalf("output = %q, want a no-agents notice", b.String())
	}
}

func TestPrintAgentInstallGuidance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		script   string
		enhanced string
		want     string
	}{
		{name: "claude", script: "curl https://example.test/install | sh", want: "curl https://example.test/install | sh"},
		{name: "codex", script: "npm install codex", enhanced: "npm install codex && npm install codex-acp", want: "npm install codex && npm install codex-acp"},
		{name: "omp", script: "curl https://example.test/omp | sh", want: "curl https://example.test/omp | sh"},
		{name: "myagent", want: "install myagent into ~/.local/bin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			agent := protocol.AgentInfo{Name: tc.name, InstallScript: tc.script, EnhancedInstallScript: tc.enhanced}
			if err := printAgentInstallGuidance(&out, agentInstallScript(agent, true)); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "aether terminal") || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("guidance = %q, want terminal and %q", out.String(), tc.want)
			}
		})
	}
}

func TestResolveAgentArgs(t *testing.T) {
	tests := []struct {
		name           string
		agent          string
		standardFlag   string
		backgroundFlag string
		shipped        bool
		input          string
		wantStandard   []string
		wantBackground []string
	}{
		{
			name:    "shipped name sends no proposal even with input available",
			agent:   "claude",
			shipped: true,
			input:   "ignored\nignored\n",
		},
		{
			name:           "flags win without prompting",
			agent:          "myagent",
			standardFlag:   "myagent --interactive {task}",
			backgroundFlag: "myagent run -p {task}",
			wantStandard:   []string{"myagent", "--interactive", "{task}"},
			wantBackground: []string{"myagent", "run", "-p", "{task}"},
		},
		{
			name:           "empty prompt input accepts defaults",
			agent:          "myagent",
			input:          "\n\n",
			wantStandard:   []string{"myagent", "{task}"},
			wantBackground: []string{"myagent", "-p", "{task}"},
		},
		{
			name:           "prompt input overrides defaults",
			agent:          "myagent",
			input:          "myagent go {task}\nmyagent quiet {task}\n",
			wantStandard:   []string{"myagent", "go", "{task}"},
			wantBackground: []string{"myagent", "quiet", "{task}"},
		},
		{
			name:           "only the missing flag is prompted",
			agent:          "myagent",
			standardFlag:   "myagent std {task}",
			input:          "myagent bg {task}\n",
			wantStandard:   []string{"myagent", "std", "{task}"},
			wantBackground: []string{"myagent", "bg", "{task}"},
		},
		{
			name:           "nil reader takes defaults without prompting",
			agent:          "myagent",
			wantStandard:   []string{"myagent", "{task}"},
			wantBackground: []string{"myagent", "-p", "{task}"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in io.Reader
			if tt.input != "" {
				in = strings.NewReader(tt.input)
			}
			standard, background, err := resolveAgentArgs(tt.agent, tt.standardFlag, tt.backgroundFlag, tt.shipped, in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(standard, tt.wantStandard) {
				t.Errorf("standard = %v, want %v", standard, tt.wantStandard)
			}
			if !reflect.DeepEqual(background, tt.wantBackground) {
				t.Errorf("background = %v, want %v", background, tt.wantBackground)
			}
		})
	}
}

func TestParseAgentAdd(t *testing.T) {
	opts, err := parseAgentAdd([]string{"myagent", "--standard", "myagent {task}"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.name != "myagent" || opts.standard != "myagent {task}" || opts.background != "" {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseAgentAdd(nil); err == nil || !strings.Contains(err.Error(), "usage: aether agent add") {
		t.Fatalf("missing name error = %v, want usage", err)
	}
	if _, err := parseAgentAdd([]string{"myagent", "--workspace", "ws"}); err == nil {
		t.Fatal("removed --workspace flag was accepted")
	}
}
