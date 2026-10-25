package harness

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCapabilityTable(t *testing.T) {
	status := map[Reporter]string{ReporterFull: "turn start and end", ReporterTurnEnd: "turn end only", ReporterNone: "none"}
	hooks := map[Hooks]string{HooksManaged: "hooks", HooksByHand: "hooks you install"}
	enhanced := map[Enhanced]string{EnhancedAdapter: "adapter", EnhancedNative: "built in", EnhancedNone: "none"}
	yes := map[bool]string{true: "yes", false: "no"}
	suggests := map[bool]string{true: "Enhanced", false: "Standard"}
	var want strings.Builder
	want.WriteString("| `--agent` | Standard status | Standard mail | Enhanced | Mode switch | Background tasks | Set up suggests |\n")
	want.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, p := range Profiles() {
		if p.Name == "custom" {
			continue
		}
		c := p.Capabilities
		var mail []string
		if c.Hooks != HooksNone {
			mail = append(mail, hooks[c.Hooks])
		}
		if c.IdleWake {
			mail = append(mail, "idle wake")
		}
		if c.Steer {
			mail = append(mail, "mid-turn steer")
		}
		if len(mail) == 0 {
			mail = []string{"none"}
		}
		fmt.Fprintf(&want, "| `%s` | %s | %s | %s | %s | %s | %s |\n", p.Name, status[c.Status], strings.Join(mail, " and "),
			enhanced[p.EnhancedSupport()], yes[c.ModeSwitch], yes[c.BackgroundTasks], suggests[c.EnhancedDefault])
	}
	doc, err := os.ReadFile("../../docs/harnesses.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "\n"+want.String()+"\n") {
		t.Fatalf("docs/harnesses.md's capability table does not match the registry; replace it with:\n\n%s", want.String())
	}
}

func TestCapabilitiesNeedTheirLaunch(t *testing.T) {
	for _, p := range Profiles() {
		c := p.Capabilities
		if c.IdleWake && c.Hooks != HooksManaged && !c.Steer {
			t.Errorf("%s: idle wake rides on the managed hooks or the app-server", p.Name)
		}
		if (c.EnhancedDefault || c.ModeSwitch || c.BackgroundTasks) && p.EnhancedSupport() == EnhancedNone {
			t.Errorf("%s: declares an Enhanced capability with no ACP command", p.Name)
		}
		if c.ModeSwitch && len(p.ResumeArgs) == 0 {
			t.Errorf("%s: switches modes but its terminal cannot resume a session", p.Name)
		}
	}
}
