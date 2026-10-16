package coordhooks

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// A Background run ends with its turn, so whatever its role it is never
// offered needs-input or waiting; a run nothing resumes while idle waits
// inside its turn instead of reporting waiting.
func TestGuidanceOffersOnlyUsableOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name         string
		g            Guide
		want, refuse []string
	}{
		{"background worker", Guide{Role: RoleWorker, Background: true},
			[]string{"never report needs-input or waiting", "inbox --wait 30"}, []string{"- needs-input", "- waiting"}},
		{"background integrator", Guide{Role: RoleIntegrator, Background: true},
			[]string{"never report needs-input or waiting", "mission plan show --wait 30"}, []string{"- needs-input", "- waiting"}},
		{"standard integrator", Guide{Role: RoleIntegrator},
			[]string{"- needs-input: only", "never report waiting", "mission plan show --wait 30"}, []string{"- waiting"}},
		{"enhanced worker", Guide{Role: RoleWorker, Wake: true},
			[]string{"- needs-input: not for workers", "- waiting: "}, []string{"never report waiting"}},
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

// The MCP brief must stand alone in the first 512 bytes of instructions.
func TestBriefFitsEveryLaunch(t *testing.T) {
	for _, role := range []string{RoleWorker, RoleIntegrator, RoleMission, RoleInteractive, RoleBackground} {
		for _, background := range []bool{false, true} {
			for _, wake := range []bool{false, true} {
				g := Guide{Role: role, Background: background, Wake: wake && !background}
				if brief := Brief(g); len(brief) > 512 || (background && strings.Contains(brief, "needs-input only")) {
					t.Errorf("Brief(%+v) is %d bytes: %s", g, len(brief), brief)
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
