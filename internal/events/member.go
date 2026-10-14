package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeMemberChanged reports a change to a member's display name. Members are
// server-wide and the timeline is per workspace, so it is published once per
// workspace.
const TypeMemberChanged Type = "member.changed"

type MemberChangedPayload struct {
	MemberID    domain.MemberID `json:"member_id"`
	DisplayName string          `json:"display_name"`
}

func (MemberChangedPayload) EventType() Type { return TypeMemberChanged }

func init() { registerPayload[MemberChangedPayload](TypeMemberChanged) }

// TypeMemberMerged reports that a merge absorbed AbsorbedID into
// SurvivorID; MergeID names its audit record. It is published under
// ServerScope.
const TypeMemberMerged Type = "member.merged"

type MemberMergedPayload struct {
	MergeID    string          `json:"merge_id"`
	SurvivorID domain.MemberID `json:"survivor_id"`
	AbsorbedID domain.MemberID `json:"absorbed_id"`
}

func (MemberMergedPayload) EventType() Type { return TypeMemberMerged }

func init() { registerPayload[MemberMergedPayload](TypeMemberMerged) }
