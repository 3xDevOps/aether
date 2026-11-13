package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MonitorCommand is the aether-server subcommand a VM's unit runs.
const MonitorCommand = "vm-monitor"

type launch struct {
	Shares []share  `json:"shares,omitempty"`
	Args   []string `json:"args"`
}

func readLaunch(dir string) (launch, error) {
	data, err := os.ReadFile(filepath.Join(dir, launchFile))
	if err != nil {
		return launch{}, fmt.Errorf("vm monitor: %w", err)
	}
	var l launch
	if err := json.Unmarshal(data, &l); err != nil {
		return launch{}, fmt.Errorf("vm monitor: decode %s: %w", launchFile, err)
	}
	return l, nil
}
