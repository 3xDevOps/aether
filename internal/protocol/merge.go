package protocol

// Member merge methods: one person who signed in two ways, and became two
// members, makes them one. A code minted on one sign-in, the minter, is
// entered on the other, the proposer, which proposes the merge; the minter
// accepts it. Every step needs an approved device, a member SSH key or a
// tailnet connection.
const (
	// MethodMemberMergeStart mints a merge code for the caller's member,
	// replacing its open one.
	MethodMemberMergeStart = "member.merge.start"
	// MethodMemberMergePreview shows the proposer what merging the code's
	// member with theirs would do, without changing anything.
	MethodMemberMergePreview = "member.merge.preview"
	// MethodMemberMergePropose records the proposer's choices on the code,
	// for the minter to accept.
	MethodMemberMergePropose = "member.merge.propose"
	// MethodMemberMergePending lists the open proposals the caller made or
	// is asked to accept.
	MethodMemberMergePending = "member.merge.pending"
	// MethodMemberMergeAccept merges, from the minter, a proposal whose
	// plan has not changed.
	MethodMemberMergeAccept = "member.merge.accept"
	// MethodMemberMergeCancel drops the caller's open codes and
	// proposals, or one proposal.
	MethodMemberMergeCancel = "member.merge.cancel"
	// MethodMemberMergeList lists the merges into the caller's member; an
	// admin sees every one.
	MethodMemberMergeList = "member.merge.list"
)

// MemberMergeStartResult is the result of member.merge.start. Code is
// shown once; the server keeps only its hash.
type MemberMergeStartResult struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}

// MemberMergePreviewParams are the params of member.merge.preview.
type MemberMergePreviewParams struct {
	Code string `json:"code"`
}

// MergeProfileHead is a member's current saved agent profile for one
// harness.
type MergeProfileHead struct {
	Harness    string `json:"harness"`
	SnapshotID string `json:"snapshot_id"`
}

// MergeHarnessDefinition is a member's custom agent definition. A plan
// carries only its name; the audit record also its definition.
type MergeHarnessDefinition struct {
	Name       string `json:"name"`
	Definition string `json:"definition,omitempty"`
}

// MergeSettings are a member's settings, the ones a merge keeps from one
// member and discards from the other. AccountSharedWith lists the members
// the member shares its agent account with; MirrorWorkspaces the
// workspaces whose mirror fetches with its GitHub connection. Home is
// whether it has a home on the server: agent logins, git signing key and
// saved files. A plan leaves out GitEmail.
type MergeSettings struct {
	DisplayName        string                   `json:"display_name"`
	Color              string                   `json:"color"`
	GitName            string                   `json:"git_name,omitempty"`
	GitEmail           string                   `json:"git_email,omitempty"`
	Image              string                   `json:"image,omitempty"`
	HarnessDefinitions []MergeHarnessDefinition `json:"harness_definitions"`
	ProfileHeads       []MergeProfileHead       `json:"profile_heads"`
	AccountSharedWith  []string                 `json:"account_shared_with"`
	MirrorWorkspaces   []string                 `json:"mirror_workspaces"`
	Home               bool                     `json:"home"`
}

// MergeIdentity is an edge account of a merging member, by login only.
type MergeIdentity struct {
	Provider string `json:"provider"`
	Login    string `json:"login"`
}

// MergeDevice is a device of a merging member: its label and status.
type MergeDevice struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
}

// MergeMember is one side of a merge as it stands.
type MergeMember struct {
	ID                   string          `json:"id"`
	DisplayName          string          `json:"display_name"`
	Role                 string          `json:"role"`
	TailnetLogin         string          `json:"tailnet_login,omitempty"`
	PublicKeyFingerprint string          `json:"public_key_fingerprint,omitempty"`
	Identities           []MergeIdentity `json:"identities"`
	Devices              []MergeDevice   `json:"devices"`
	Runs                 int             `json:"runs"`
	Schedules            int             `json:"schedules"`
	Settings             MergeSettings   `json:"settings"`
}

// MergeChoice is the outcome of keeping one member's settings: that member
// survives with its id, and the other's settings in Discarded are
// recorded in the merge's audit row and dropped.
type MergeChoice struct {
	KeepSettingsOf string        `json:"keep_settings_of"`
	AbsorbedID     string        `json:"absorbed_id"`
	Discarded      MergeSettings `json:"discarded"`
}

// MemberMergePlan is what merging two members does. Minted is the member
// that minted the code, Proposer the one that entered it. Role is the
// merged member's role: the lower of the two. RoleWithKeepAdmin is admin
// when one of them is, the role the admin side gets by asking with
// keep_admin, and KeepAdminRequired is set when only that keeps an admin
// on the server. SignIns are every way the merged member will reach the
// server. PlanDigest names the state the plan was computed from.
type MemberMergePlan struct {
	Minted            MergeMember   `json:"minted"`
	Proposer          MergeMember   `json:"proposer"`
	Role              string        `json:"role"`
	RoleWithKeepAdmin string        `json:"role_with_keep_admin,omitempty"`
	KeepAdminRequired bool          `json:"keep_admin_required,omitempty"`
	SignIns           []string      `json:"sign_ins"`
	Choices           []MergeChoice `json:"choices"`
	PlanDigest        string        `json:"plan_digest"`
}

// MemberMergeProposeParams are the params of member.merge.propose.
// KeepSettingsOf is the id of the member whose settings prevail, the
// survivor. DiscardAck must be true: the proposer has seen what is
// discarded. KeepAdmin counts only from an admin proposer.
type MemberMergeProposeParams struct {
	Code           string `json:"code"`
	KeepSettingsOf string `json:"keep_settings_of"`
	PlanDigest     string `json:"plan_digest"`
	DiscardAck     bool   `json:"discard_ack"`
	KeepAdmin      bool   `json:"keep_admin,omitempty"`
}

// MemberMergeProposal is an open proposal, the result of
// member.merge.propose. Plan is recomputed when read; Refusal says why
// accepting it would now be refused.
type MemberMergeProposal struct {
	ID                string           `json:"id"`
	MintedID          string           `json:"minted_id"`
	ProposerID        string           `json:"proposer_id"`
	KeepSettingsOf    string           `json:"keep_settings_of"`
	ProposerKeepAdmin bool             `json:"proposer_keep_admin,omitempty"`
	PlanDigest        string           `json:"plan_digest"`
	ExpiresAt         string           `json:"expires_at"`
	Plan              *MemberMergePlan `json:"plan,omitempty"`
	Refusal           string           `json:"refusal,omitempty"`
}

// MemberMergePendingResult is the result of member.merge.pending.
type MemberMergePendingResult struct {
	Proposals []MemberMergeProposal `json:"proposals"`
}

// MemberMergeAcceptParams are the params of member.merge.accept.
// PlanDigest is the proposal's. KeepAdmin counts only from an admin
// minter.
type MemberMergeAcceptParams struct {
	ProposalID string `json:"proposal_id"`
	PlanDigest string `json:"plan_digest"`
	KeepAdmin  bool   `json:"keep_admin,omitempty"`
}

// MemberMergeCancelParams are the params of member.merge.cancel: one
// proposal, or with none every open code and proposal of the caller.
type MemberMergeCancelParams struct {
	ProposalID string `json:"proposal_id,omitempty"`
}

// MemberMergeCancelResult is the result of member.merge.cancel.
type MemberMergeCancelResult struct {
	Cancelled int `json:"cancelled"`
}

// MemberMerge is the audit record of one merge. Discarded holds the
// absorbed member's settings and Absorbed its member as it was;
// ArchivePath is where its home was moved, empty when it had none.
type MemberMerge struct {
	ID                 string        `json:"id"`
	SurvivorID         string        `json:"survivor_id"`
	AbsorbedID         string        `json:"absorbed_id"`
	ConfirmedByKind    string        `json:"confirmed_by_kind"`
	ConfirmedBy        string        `json:"confirmed_by"`
	SurvivorRoleBefore string        `json:"survivor_role_before"`
	AbsorbedRoleBefore string        `json:"absorbed_role_before"`
	RoleAfter          string        `json:"role_after"`
	Discarded          MergeSettings `json:"discarded"`
	Absorbed           MergeMember   `json:"absorbed"`
	ArchivePath        string        `json:"archive_path,omitempty"`
	CreatedAt          string        `json:"created_at"`
}

// MemberMergeAcceptResult is the result of member.merge.accept: the
// merge and the merged member.
type MemberMergeAcceptResult struct {
	Merge  MemberMerge `json:"merge"`
	Member Member      `json:"member"`
}

// MemberMergeListResult is the result of member.merge.list, newest first.
type MemberMergeListResult struct {
	Merges []MemberMerge `json:"merges"`
}
