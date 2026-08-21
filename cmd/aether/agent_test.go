package main

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// The list output must distinguish shipped profiles from member-registered
// definitions so members can tell what agent add actually stored.
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

func TestResolveAgentArgs(t *testing.T) {
	tests := []struct {
		name         string
		agent        string
		tuiFlag      string
		headlessFlag string
		shipped      bool
		input        string
		wantTUI      []string
		wantHeadless []string
	}{
		{
			name:    "shipped name sends no proposal even with input available",
			agent:   "claude",
			shipped: true,
			input:   "ignored\nignored\n",
		},
		{
			name:         "flags win without prompting",
			agent:        "myagent",
			tuiFlag:      "myagent --interactive {task}",
			headlessFlag: "myagent run -p {task}",
			wantTUI:      []string{"myagent", "--interactive", "{task}"},
			wantHeadless: []string{"myagent", "run", "-p", "{task}"},
		},
		{
			name:         "empty prompt input accepts defaults",
			agent:        "myagent",
			input:        "\n\n",
			wantTUI:      []string{"myagent", "{task}"},
			wantHeadless: []string{"myagent", "-p", "{task}"},
		},
		{
			name:         "prompt input overrides defaults",
			agent:        "myagent",
			input:        "myagent go {task}\nmyagent quiet {task}\n",
			wantTUI:      []string{"myagent", "go", "{task}"},
			wantHeadless: []string{"myagent", "quiet", "{task}"},
		},
		{
			name:         "only the missing flag is prompted",
			agent:        "myagent",
			tuiFlag:      "myagent tui {task}",
			input:        "myagent hl {task}\n",
			wantTUI:      []string{"myagent", "tui", "{task}"},
			wantHeadless: []string{"myagent", "hl", "{task}"},
		},
		{
			name:         "nil reader takes defaults without prompting",
			agent:        "myagent",
			wantTUI:      []string{"myagent", "{task}"},
			wantHeadless: []string{"myagent", "-p", "{task}"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in io.Reader
			if tt.input != "" {
				in = strings.NewReader(tt.input)
			}
			tui, headless, err := resolveAgentArgs(tt.agent, tt.tuiFlag, tt.headlessFlag, tt.shipped, in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tui, tt.wantTUI) {
				t.Errorf("tui = %v, want %v", tui, tt.wantTUI)
			}
			if !reflect.DeepEqual(headless, tt.wantHeadless) {
				t.Errorf("headless = %v, want %v", headless, tt.wantHeadless)
			}
		})
	}
}

func TestParseAgentAdd(t *testing.T) {
	opts, err := parseAgentAdd([]string{"myagent", "--workspace", "ws", "--tui", "myagent {task}"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.name != "myagent" || opts.workspace != "ws" || opts.tui != "myagent {task}" || opts.headless != "" {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseAgentAdd(nil); err == nil || !strings.Contains(err.Error(), "usage: aether agent add") {
		t.Fatalf("missing name error = %v, want usage", err)
	}
}
