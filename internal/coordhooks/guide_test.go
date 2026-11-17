package coordhooks

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/coordtransport"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// A Background run ends with its turn, so whatever its role it is never
// offered needs-input, waiting or answered; a run nothing resumes while idle
// waits inside its turn instead of reporting waiting. Every other run ends
// each turn with a report, answered when nothing is pending.
func TestGuidanceOffersOnlyUsableOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name         string
		g            Guide
		want, refuse []string
	}{
		{"background", Guide{Role: RoleBackground, Background: true},
			[]string{"never report needs-input, waiting or answered", "inbox --wait 30"}, []string{"- needs-input", "- waiting", "- answered"}},
		{"standard interactive", Guide{Role: RoleInteractive},
			[]string{"END EVERY TURN WITH EXACTLY ONE REPORT", "- answered: ", "- needs-input: only", "use needs-input", "never report waiting"},
			[]string{"- waiting", "--for"}},
		{"enhanced interactive", Guide{Role: RoleInteractive, Wake: true},
			[]string{"- answered: ", "- waiting: ", "use needs-input", "Not while aether_status shows protected", "reply as message text before the report", "Say the result only there, none after"},
			[]string{"without a report", "after the report"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := RoleCard(tc.g)
			for _, want := range tc.want {
				if !strings.Contains(card, want) {
					t.Errorf("card lacks %q: %s", want, card)
				}
			}
			for _, refuse := range tc.refuse {
				if strings.Contains(card, refuse) {
					t.Errorf("card carries %q: %s", refuse, card)
				}
			}
		})
	}
}

// The MCP brief must stand alone in the first 512 bytes of instructions, and
// the role card's reporting contract stays short enough to read every turn.
func TestBriefFitsEveryLaunch(t *testing.T) {
	for _, role := range []string{RoleInteractive, RoleBackground} {
		for _, background := range []bool{false, true} {
			for _, wake := range []bool{false, true} {
				for _, cli := range []string{OldCLI, NewCLI} {
					g := Guide{Role: role, Background: background, Wake: wake && !background, CLI: cli}
					brief := Brief(g)
					if len(brief) > 512 || !strings.Contains(brief, "End every turn with exactly one aether_report") ||
						(background && strings.Contains(brief, "needs-input =")) || background == strings.Contains(brief, "answered =") {
						t.Errorf("Brief(%+v) is %d bytes: %s", g, len(brief), brief)
					}
					if guide := ReportGuide(g); len(guide) > 1536 {
						t.Errorf("ReportGuide(%+v) is %d bytes: %s", g, len(guide), guide)
					}
				}
			}
		}
	}
}

// guideTexts renders every guide text for the runs launches make.
func guideTexts(cli string) string {
	var b strings.Builder
	for _, g := range []Guide{
		{RunID: "run-self"},
		{RunID: "run-self", Role: RoleInteractive},
		{RunID: "run-self", Role: RoleInteractive, Wake: true},
		{RunID: "run-self", Role: RoleBackground, Background: true},
		{RunID: "run-self", Role: RoleInteractive, Wake: true, Parent: "run-parent", Repos: "docs at /workspace/docs"},
	} {
		g.CLI = cli
		fmt.Fprintf(&b, "=== role=%q background=%t wake=%t parent=%q\n--- brief\n%s\n--- reminder\n%s\n--- card\n%s\n", g.Role, g.Background, g.Wake, g.Parent, Brief(g), ReportReminder(g), RoleCard(g))
	}
	return b.String()
}

// A container without the new path, and every caller that names no
// command, gets the text main produced before the new name existed, byte
// for byte: testdata/old-name.golden is guideTexts as main rendered it.
func TestOldNameTextIsUnchanged(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "old-name.golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cli := range []string{"", OldCLI} {
		if got := guideTexts(cli); got != string(want) {
			t.Errorf("guide texts for CLI %q differ from testdata/old-name.golden:\n%s", cli, got)
		}
	}
}

// A container with the new path is told the new name everywhere but in the
// turn-end reminder's lead, which released dashboards match, and in text
// the guide quotes.
func TestNewNameText(t *testing.T) {
	g := Guide{RunID: "run-self", Role: RoleInteractive, Parent: "run-parent", Repos: "aether-internal at /workspace/aether-internal", CLI: NewCLI}
	for name, text := range map[string]string{"brief": Brief(g), "guide": ReportGuide(g), "wait": WaitRule(g)} {
		if !strings.Contains(text, NewCLI) || strings.Contains(text, OldCLI) {
			t.Errorf("%s names the old command or not the new one:\n%s", name, text)
		}
	}
	card := RoleCard(g)
	if quoted := strings.ReplaceAll(card, g.Repos, ""); !strings.Contains(card, g.Repos) || strings.Contains(quoted, OldCLI) ||
		!strings.Contains(card, "`cloudfield-internal ask --to run-parent`") || !strings.Contains(card, "`cloudfield-internal skill`") {
		t.Errorf("role card does not name the new command outside the repositories it quotes:\n%s", card)
	}
	if reminder := ReportReminder(g); !strings.HasPrefix(reminder, ReportReminderLead) {
		t.Errorf("report reminder lost its lead: %s", reminder)
	}
}

// Only an executable file at the new path makes text name it.
func TestLocalCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows files carry no executable bit; the session CLI runs in linux containers only")
	}
	dir := t.TempDir()
	exe, plain := filepath.Join(dir, "exe"), filepath.Join(dir, "plain")
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{exe: NewCLI, plain: OldCLI, dir: OldCLI, filepath.Join(dir, "missing"): OldCLI} {
		if got := LocalCLI(path); got != want {
			t.Errorf("LocalCLI(%s) = %s, want %s", path, got, want)
		}
	}
}

// Mail never wakes a protected run, so it waits in its turn instead.
func TestProtectedRunWaitsInItsTurn(t *testing.T) {
	t.Setenv(coordtransport.EnhancedEnv, "1")
	t.Setenv(coordtransport.BackgroundEnv, "")
	for _, protected := range []bool{false, true} {
		g := StatusGuide(protocol.CoordStatusResult{RunID: "run-1", Capabilities: []string{protocol.MethodCoordReport}, Protected: protected})
		if card := RoleCard(g); strings.Contains(card, "- waiting:") == protected || strings.Contains(card, "never report waiting") != protected {
			t.Errorf("protected %v: card %s", protected, card)
		}
	}
}
