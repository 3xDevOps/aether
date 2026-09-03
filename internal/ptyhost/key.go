package ptyhost

import (
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SessionKey identifies a persistent PTY session independently of its owner.
type SessionKey string

// RunSession returns the key for an agent run's PTY session.
func RunSession(run domain.RunID) SessionKey { return SessionKey(run) }

// TerminalSession returns the key for a member terminal tab.
func TerminalSession(member domain.MemberID, tab string) SessionKey {
	return SessionKey("terminal:" + string(member) + ":" + tab)
}

// RunShellSession returns the key for a shell tab inside a run container.
func RunShellSession(run domain.RunID, tab string) SessionKey {
	return SessionKey("run-shell:" + string(run) + ":" + tab)
}

// Run returns the run ID when k identifies an agent run session.
func (k SessionKey) Run() (domain.RunID, bool) {
	if strings.HasPrefix(string(k), "terminal:") || strings.HasPrefix(string(k), "run-shell:") {
		return "", false
	}
	return domain.RunID(k), true
}
