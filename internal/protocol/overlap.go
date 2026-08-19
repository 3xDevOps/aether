package protocol

// MethodRunOverlaps lists the conflict radar's cross-run file overlaps.
// It is a View-level read: no capability, no target.
const MethodRunOverlaps = "run.overlaps"

// OverlapPeer is one other active run touching files a run also touches,
// with the member who owns it (for member-colored attribution).
type OverlapPeer struct {
	RunID    string   `json:"run_id"`
	MemberID string   `json:"member_id"`
	Files    []string `json:"files"`
}

// Overlap is one run's whole overlap set. Every overlapping pair is
// reported from both sides, so a client can key overlaps by RunID alone.
type Overlap struct {
	RunID string        `json:"run_id"`
	With  []OverlapPeer `json:"with"`
}

// RunOverlapsResult is the result of run.overlaps. Runs with no overlap
// are absent.
type RunOverlapsResult struct {
	Overlaps []Overlap `json:"overlaps"`
}
