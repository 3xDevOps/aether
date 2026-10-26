package coordhooks

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
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
		{"background worker", Guide{Role: RoleWorker, Background: true},
			[]string{"never report needs-input, waiting or answered", "inbox --wait 30"}, []string{"- needs-input", "- waiting", "- answered"}},
		{"background integrator", Guide{Role: RoleIntegrator, Background: true},
			[]string{"never report needs-input, waiting or answered", "mission plan show --wait 30"}, []string{"- needs-input", "- waiting", "- answered"}},
		{"standard integrator", Guide{Role: RoleIntegrator},
			[]string{"END EVERY TURN WITH EXACTLY ONE REPORT", "- answered: ", "- needs-input: only", "use needs-input", "never report waiting", "mission plan show --wait 30"},
			[]string{"- waiting", "--for"}},
		{"enhanced worker", Guide{Role: RoleWorker, Wake: true},
			[]string{"END EVERY TURN WITH EXACTLY ONE REPORT", "[--for agents|ci|process]", "- answered: ", "- needs-input: not for workers", "- waiting: ", "ask your integrator"},
			[]string{"never report waiting", "use needs-input"}},
		{"enhanced interactive", Guide{Role: RoleInteractive, Wake: true},
			[]string{"- answered: ", "- waiting: ", "use needs-input", "Not while aether_status shows protected"}, []string{"without a report"}},
		{"mission", Guide{Role: RoleMission},
			[]string{"an integrator uses needs-input, a worker `aether-internal ask`"}, []string{"stop; use needs-input"}},
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
	for _, role := range []string{RoleWorker, RoleIntegrator, RoleMission, RoleInteractive, RoleBackground} {
		for _, background := range []bool{false, true} {
			for _, wake := range []bool{false, true} {
				g := Guide{Role: role, Background: background, Wake: wake && !background}
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
