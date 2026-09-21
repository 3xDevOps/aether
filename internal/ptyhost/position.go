package ptyhost

// TerminalEpoch identifies one continuous logical terminal output stream.
// It changes whenever continuity with a prior stream cannot be proven.
type TerminalEpoch string

// TerminalSequence is the absolute number of client-visible output bytes
// published in a terminal epoch.
type TerminalSequence uint64

// TerminalPosition identifies an exact byte boundary in a terminal stream.
// Bytes resumed from this position start strictly after Sequence.
type TerminalPosition struct {
	Epoch    TerminalEpoch    `json:"epoch"`
	Sequence TerminalSequence `json:"sequence"`
}

func newTerminalEpoch() (TerminalEpoch, error) {
	id, err := newResumeID()
	return TerminalEpoch(id), err
}

// TerminalPositionWriter receives the exact position held after attach replay.
// resumed reports whether the requested delta was served without rebuilding.
type TerminalPositionWriter interface {
	SetTerminalPosition(position TerminalPosition, resumed bool)
}
