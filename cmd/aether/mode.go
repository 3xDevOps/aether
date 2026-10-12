package main

import "fmt"

var launchModes = map[string]string{"standard": "tui", "enhanced": "acp", "background": "headless"}

const launchModeHelp = "standard, enhanced or background"

const modelHelp = "agent model, passed to the agent as is; empty keeps the agent's default"

func parseLaunchMode(name string) (string, error) {
	if mode, ok := launchModes[name]; ok {
		return mode, nil
	}
	return "", fmt.Errorf("invalid mode %q (want %s)", name, launchModeHelp)
}

func modeName(wire string) string {
	for name, mode := range launchModes {
		if mode == wire {
			return name
		}
	}
	return wire
}
