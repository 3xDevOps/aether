package main

import "fmt"

var launchModes = map[string]string{
	"standard": "tui", "enhanced": "acp", "background": "headless",
	"tui": "tui", "acp": "acp", "headless": "headless",
}

const launchModeHelp = "standard (tui), enhanced (acp) or background (headless)"

func parseLaunchMode(name string) (string, error) {
	if mode, ok := launchModes[name]; ok {
		return mode, nil
	}
	return "", fmt.Errorf("invalid mode %q (want %s)", name, launchModeHelp)
}
