package localops

import (
	"os/exec"

	"github.com/3xDevOps/Aether/internal/harness"
)

// ScanModeProfile asks which local agent configurations are worth importing.
const ScanModeProfile = "profile"

// HarnessStatus is one setup-capable harness's local availability.
type HarnessStatus struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
}

// DetectHarnesses reports whether each setup-capable harness executable is on
// this machine's PATH.
func DetectHarnesses() []HarnessStatus {
	profiles := harness.SetupHarnesses()
	out := make([]HarnessStatus, 0, len(profiles))
	for _, p := range profiles {
		_, err := exec.LookPath(p.HeadlessArgs[0])
		out = append(out, HarnessStatus{Name: p.Name, Installed: err == nil})
	}
	return out
}
