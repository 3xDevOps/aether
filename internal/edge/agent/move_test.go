package edgeagent

import "testing"

// The address in use is refused in any spelling, before anything dials.
func TestMoveRefusesTheAddressInUseInAnySpelling(t *testing.T) {
	a, err := New(Config{EdgeURL: "https://edge.cloudfield.ai", DataDir: t.TempDir(), HostKey: newHostKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"https://edge.cloudfield.ai", "https://EDGE.cloudfield.ai:443/"} {
		if _, err := a.Move(t.Context(), target); err == nil || err.Error() != "edge-url already is https://edge.cloudfield.ai" {
			t.Errorf("move to %s: %v", target, err)
		}
	}
}
