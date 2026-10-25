package acphost

// Spend reports the running USD cost of the agent's session and the tokens of
// the turns since the previous Spend. FromZero marks a total that counts from
// zero again: in a new session, after a Claude Code prompt that starts one, or
// below the previous total.
type Spend struct {
	InputTokens  int64
	OutputTokens int64
	Total        float64
	FromZero     bool
}

// claudeCostResets start a new Claude Code session in place of the ACP
// session's, with a running cost that counts from zero.
var claudeCostResets = []string{"/clear", "/reset", "/new"}

// reportSpendLocked reports a total the agent reported since the previous
// Spend. Tokens without one are dropped, so a run whose agent reports no cost
// stays unmetered instead of free.
func (s *Session) reportSpendLocked() {
	sp, due := s.spend, s.spendDue
	s.spend, s.spendDue = Spend{Total: sp.Total}, false
	if due && s.cfg.OnSpend != nil {
		s.callback(func() { s.cfg.OnSpend(sp) })
	}
}
