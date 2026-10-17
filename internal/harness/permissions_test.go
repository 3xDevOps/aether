package harness

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
)

type noPrompt struct {
	// flag must also be in the ACP argv when inACP is set.
	flag          string
	inACP         bool
	env           map[string]string
	acpMode       string
	permissionEnv map[string]string
	optOut        bool
	// modes are the ACP session modes, for an agent whose session cannot
	// be recorded without a login; others come from acpmock's fixture.
	modes []string
}

var noPromptSettings = map[string]noPrompt{
	"claude": {
		flag:    "--dangerously-skip-permissions",
		env:     map[string]string{"IS_SANDBOX": "1"},
		acpMode: "bypassPermissions",
	},
	"codex": {
		flag:    "--dangerously-bypass-approvals-and-sandbox",
		acpMode: "agent-full-access",
	},
	"omp": {flag: "--auto-approve", inACP: true},
	"opencode": {
		permissionEnv: map[string]string{"OPENCODE_CONFIG_CONTENT": `{"permission":"allow"}`},
	},
	"pi": {optOut: true},
	"copilot": {
		flag:  "--allow-all",
		inACP: true,
		env:   map[string]string{"COPILOT_ALLOW_ALL": "true"},
	},
	// buildAvailableModes in gemini-cli 0.64.0.
	"gemini": {
		flag:    "--approval-mode=yolo",
		env:     map[string]string{"GEMINI_CLI_TRUST_WORKSPACE": "true"},
		acpMode: "yolo",
		modes:   []string{"default", "autoEdit", "yolo", "plan"},
	},
	"cursor":      {flag: "--force", inACP: true},
	"antigravity": {flag: "--dangerously-skip-permissions"},
}

func TestEveryShippedAgentRunsWithoutPermissionPrompts(t *testing.T) {
	for _, p := range Profiles() {
		if p.Name == "custom" {
			continue
		}
		want, ok := noPromptSettings[p.Name]
		if !ok {
			t.Errorf("%s ships without a no-prompt setting in noPromptSettings", p.Name)
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			if want.flag == "" && want.permissionEnv == nil && !want.optOut {
				t.Fatal("no way to turn its permission prompts off")
			}
			if p.NoPermissionPrompt != want.optOut {
				t.Errorf("NoPermissionPrompt = %v, want %v", p.NoPermissionPrompt, want.optOut)
			}
			if want.flag != "" {
				argvs := map[string][]string{"tui": p.TUIArgs, "headless": p.HeadlessArgs, "resume": p.ResumeArgs}
				if want.inACP {
					argvs["acp"] = p.ACPArgs
				}
				for mode, argv := range argvs {
					if len(argv) > 0 && !slices.Contains(argv, want.flag) {
						t.Errorf("%s argv %v lacks %s", mode, argv, want.flag)
					}
				}
			}
			for key, value := range want.env {
				if p.Env[key] != value {
					t.Errorf("Env[%s] = %q, want %q for %s", key, p.Env[key], value, want.flag)
				}
			}
			if !maps.Equal(p.PermissionEnv, want.permissionEnv) {
				t.Errorf("PermissionEnv = %v, want %v", p.PermissionEnv, want.permissionEnv)
			}
			if p.ACPMode != want.acpMode {
				t.Errorf("ACPMode = %q, want %q", p.ACPMode, want.acpMode)
			}
			if want.acpMode == "" {
				return
			}
			modes := want.modes
			if modes == nil {
				modes = recordedModes(t, p.Name)
			}
			if !slices.Contains(modes, want.acpMode) {
				t.Errorf("%s does not offer mode %q", p.Name, want.acpMode)
			}
		})
	}
}

func recordedModes(t *testing.T, name string) []string {
	t.Helper()
	fixture, err := acpmock.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Modes struct {
			AvailableModes []struct {
				ID string `json:"id"`
			} `json:"availableModes"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(fixture.SessionNew, &session); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, mode := range session.Modes.AvailableModes {
		ids = append(ids, mode.ID)
	}
	return ids
}
