package main

import (
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestPrintAgents(t *testing.T) {
	found, missing := true, false
	var b strings.Builder
	err := printAgents(&b, []protocol.AgentInfo{
		{Name: "claude", Source: "shipped", Installed: true, Enhanced: "adapter", LoginFound: &missing},
		{Name: "codex", Source: "shipped", Installed: true, Enhanced: "adapter", EnhancedInstalled: true, LoginFound: &found},
		{Name: "myagent", Source: "member", Enhanced: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "AGENT    SOURCE   INSTALLED  ENHANCED       LOGIN FILE\n" +
		"claude   shipped  yes        not installed  not found\n" +
		"codex    shipped  yes        installed      found\n" +
		"myagent  member   no         none           -\n"
	if b.String() != want {
		t.Fatalf("output =\n%s\nwant\n%s", b.String(), want)
	}
}

// loginTab stands in for a terminal tab: it records what is typed and
// answers with what a PTY sends back.
type loginTab struct {
	io.Reader
	typed strings.Builder
}

func (l *loginTab) Write(p []byte) (int, error) { return l.typed.Write(p) }

func TestFollowLoginPassesPlainLinesAndTheExitStatus(t *testing.T) {
	const command = "codex login --device-auth"
	pty := "\x1b]0;root@env: ~\x07\x1b[?2004hroot@env:~# codex login --device-auth; echo aether-login-exit $?; exit\r\n\x1b[?2004l\r" +
		"\x1b[1mWelcome to Codex\x1b[0m\r\n" +
		"1. Open this link in your browser\r\n   \x1b[94mhttps://auth.openai.com/codex/device\x1b[0m\r\n" +
		"2. Enter this one-time code \x1b[90m(expires in 15 minutes)\x1b[0m\r\n   \x1b[94mABCD-EFGH\x1b[0m\r\n" +
		"⠋ Waiting\r⠙ Waiting\r\x1b[2K\x1b[1GSuccessfully logged in\r\n"
	want := "root@env:~# codex login --device-auth; echo aether-login-exit $?; exit\n" +
		"Welcome to Codex\n" +
		"1. Open this link in your browser\n" +
		"   https://auth.openai.com/codex/device\n" +
		"2. Enter this one-time code (expires in 15 minutes)\n" +
		"   ABCD-EFGH\n" +
		"Successfully logged in\n" +
		"logout\n"
	typed := command + "; echo aether-login-exit $?; exit\n"
	for _, tc := range []struct {
		name string
		tail string
		exit int
	}{
		{name: "new sign-in", tail: "aether-login-exit 0\r\nlogout\r\n", exit: 0},
		{name: "failed sign-in", tail: "aether-login-exit 1\r\nlogout\r\n", exit: 1},
		{name: "tab ended early", tail: "logout", exit: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tab := &loginTab{Reader: strings.NewReader(pty + tc.tail)}
			var out strings.Builder
			exit, err := followLogin(tab, command, &out)
			if err != nil {
				t.Fatal(err)
			}
			if exit != tc.exit || tab.typed.String() != typed {
				t.Fatalf("exit %d, typed %q; want %d, %q", exit, tab.typed.String(), tc.exit, typed)
			}
			if out.String() != want {
				t.Fatalf("output =\n%s\nwant\n%s", out.String(), want)
			}
		})
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

func TestAgentInstallGuidance(t *testing.T) {
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
			agent := protocol.AgentInfo{Name: tc.name, InstallScript: tc.script, EnhancedInstallScript: tc.enhanced}
			got := terminalGuidanceError("open the terminal", errors.New("dial: refused"), agentInstallScript(agent, true)).Error()
			if !strings.Contains(got, "aether terminal") || !strings.Contains(got, tc.want) {
				t.Fatalf("guidance = %q, want terminal and %q", got, tc.want)
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
			name:           "background default follows the standard command",
			agent:          "mycursor",
			standardFlag:   "agent {task}",
			wantStandard:   []string{"agent", "{task}"},
			wantBackground: []string{"agent", "-p", "{task}"},
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
	if _, _, err := resolveAgentArgs("myagent", "   ", "", false, nil); err == nil || err.Error() != "--standard is blank" {
		t.Fatalf("blank --standard error = %v, want --standard is blank", err)
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

func TestAgentRemoveUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"remove"}, {"remove", "a", "b"}} {
		if err := runAgent(args); err == nil || !strings.Contains(err.Error(), "usage: aether agent ") {
			t.Fatalf("runAgent(%v) error = %v, want usage", args, err)
		}
	}
}

func TestParseAgentAddLoginUnderLocalHome(t *testing.T) {
	t.Setenv("HOME", "/home/alice")
	t.Setenv("USERPROFILE", "/home/alice")
	opts, err := parseAgentAdd([]string{"mycursor", "--login", "/home/alice/.cursor/auth.json", "--login", "~/.config/x.json"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"~/.cursor/auth.json", "~/.config/x.json"}; !slices.Equal(opts.login, want) {
		t.Fatalf("login = %v, want %v: a shell-expanded local home goes back to ~/", opts.login, want)
	}
}

func TestTerminalGuidanceErrorKeepsTheCause(t *testing.T) {
	cause := errors.New("start terminal: image pull failed")
	err := terminalGuidanceError("open the environment terminal", cause, "npm install codex")
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "image pull failed") || !strings.Contains(err.Error(), "aether terminal") || !strings.Contains(err.Error(), "npm install codex") {
		t.Fatalf("err = %v, want the cause and the guidance", err)
	}
}
