package acphost

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Adoption is what the keeper reported about the session a server takes
// over: the agent's initialize and session results, and the item log turn of
// the session/prompt it has in flight, zero for none. ReplayWait bounds how
// long answers wait for the keeper to send again the requests the log
// leaves unanswered.
type Adoption struct {
	Initialize json.RawMessage
	Session    json.RawMessage
	SessionID  string
	PromptTurn int64
	ReplayWait time.Duration
}

// adopt takes over the session the keeper reported. A turn the log leaves
// open goes on: its prompt's answer still reaches this session, or the
// agent's own turn still streams; neither is sent again.
func (s *Session) adopt(openTurn bool) error {
	a := s.cfg.Adopt
	var init initializeResult
	if err := json.Unmarshal(a.Initialize, &init); err != nil {
		return fmt.Errorf("acphost: adopt the agent's initialize result: %w", err)
	}
	if err := s.conn.setInfo(init); err != nil {
		return err
	}
	var res sessionResult
	if err := json.Unmarshal(a.Session, &res); err != nil || a.SessionID == "" {
		return fmt.Errorf("acphost: adopt session %q: %w", a.SessionID, errors.Join(err, errors.New("the keeper reported no session")))
	}
	s.conn.sessionID.Store(a.SessionID)
	s.conn.reuseRequests(PendingRequests(s.log), a.ReplayWait)
	started := LastItem(s.log, KindTurnStart)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionOptionsLocked(res)
	s.restoreStateLocked()
	s.inputsLocked()
	switch {
	case openTurn && started.Autonomous:
		s.autonomous, s.turnAt, s.lastUpdate = true, time.Now(), time.Now()
		s.settleLocked()
	case openTurn && a.PromptTurn == started.Turn:
		s.turnActive, s.turnAt = true, time.Now()
		s.stateLocked(TurnState{Working: true, Reason: "prompt"})
		go s.runTurn(nil, &turnAck{done: make(chan struct{})}, func() {})
	case openTurn:
		// The server went away before the turn's prompt reached the keeper,
		// whose prompt, if any, is an earlier turn's.
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: "Aether's server restarted before this turn's prompt reached the agent. Send it again."}})
		s.emitLocked(Item{Kind: KindTurnEnd, StopReason: "interrupted"})
		s.stateLocked(TurnState{Reason: "interrupted"})
	default:
		s.stateLocked(TurnState{})
	}
	return nil
}

// restoreStateLocked takes the mode, options, commands, auth status and
// usage an adopted session last recorded, so they are not recorded again.
func (s *Session) restoreStateLocked() {
	if it := LastItem(s.log, KindModeChange); it.Mode != "" {
		s.state.Mode = it.Mode
		s.options.setCurrent("mode", it.Mode)
	}
	if it := LastItem(s.log, KindConfigOptions); present(it.ConfigOptions) {
		s.options.adopt(it.ConfigOptions)
		s.state.ConfigOptions = it.ConfigOptions
	}
	s.state.Commands = LastItem(s.log, KindCommands).Commands
	s.state.Auth = LastItem(s.log, KindAuthStatus).Auth
	s.state.Usage = LastItem(s.log, KindUsage).Usage
}

// Detach makes the end of the connection silent: no item, state, input or
// delivery callback records it, because the agent keeps running under its
// keeper and the next server process adopts the session. No turn starts after
// it. Call it before the connection ends.
func (s *Session) Detach() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detached = true
}
