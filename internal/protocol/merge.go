package protocol

// Member merge methods: one person who signed in two ways, and became two
// members, makes them one. A code minted on one sign-in and entered on the
// other proves the same person holds both.
const (
	// MethodMemberMergeStart mints a merge code for the caller's member,
	// replacing its open one: any approved member, and under
	// approved-devices not on a connection that signed in with a device no
	// person approved.
	MethodMemberMergeStart = "member.merge.start"
	// MethodMemberMergePreview shows what confirming a merge code from the
	// caller's member would do, without changing anything.
	MethodMemberMergePreview = "member.merge.preview"
	// MethodMemberMergeConfirm merges the code's member and the caller's
	// into the one whose settings prevail, provided the plan is still the
	// one previewed.
	MethodMemberMergeConfirm = "member.merge.confirm"
	// MethodMemberMergeList lists the merges into the caller's member; an
	// admin sees every one.
	MethodMemberMergeList = "member.merge.list"
)

// MemberMergeStartParams are the params of member.merge.start. KeepAdmin,
// for an admin only, consents to the merged member staying admin when the
// other member confirms with keep_admin.
type MemberMergeStartParams struct {
	KeepAdmin bool `json:"keep_admin,omitempty"`
}

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

// MergeHarnessDefinition is a member's custom agent definition, as
// the server stores it.
type MergeHarnessDefinition struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

// MergeSettings are a member's settings, the ones a merge keeps from one
// member and discards from the other. AccountSharedWith lists the members
// the member shares its agent account with; MirrorWorkspaces the
// workspaces whose mirror fetches with its GitHub connection. Home is
// whether it has a home on the server: agent logins, git signing key and
// saved files.
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

// MergeMember is one side of a merge as it stands.
type MergeMember struct {
	ID                   string        `json:"id"`
	DisplayName          string        `json:"display_name"`
	Role                 string        `json:"role"`
	TailnetLogin         string        `json:"tailnet_login,omitempty"`
	PublicKeyFingerprint string        `json:"public_key_fingerprint,omitempty"`
	Identities           []Identity    `json:"identities"`
	Devices              []Device      `json:"devices"`
	Runs                 int           `json:"runs"`
	Schedules            int           `json:"schedules"`
	Settings             MergeSettings `json:"settings"`
}

// MergeChoice is the outcome of keeping one member's settings: that member
// survives with its id, and the other's settings in Discarded are
// recorded in the merge's audit row and dropped.
type MergeChoice struct {
	KeepSettingsOf string        `json:"keep_settings_of"`
	AbsorbedID     string        `json:"absorbed_id"`
	Discarded      MergeSettings `json:"discarded"`
}

// MemberMergePlan is the result of member.merge.preview. Minted is the
// member that minted the code and Confirming the caller's. Role is the
// merged member's role: the lower of the two. RoleWithKeepAdmin is admin
// when one of them is, the role keep_admin asks for, and
// KeepAdminRequired is set when only keep_admin keeps an admin on the
// server. KeepAdminConsented is set when the admin side asked for it:
// the caller is the admin, or the admin minted the code with keep_admin;
// confirm honours keep_admin only then. SignIns are every way the merged member will reach the server.
// PlanDigest names the state the plan was computed from; confirm refuses
// once it changed.
type MemberMergePlan struct {
	Minted             MergeMember   `json:"minted"`
	Confirming         MergeMember   `json:"confirming"`
	Role               string        `json:"role"`
	RoleWithKeepAdmin  string        `json:"role_with_keep_admin,omitempty"`
	KeepAdminRequired  bool          `json:"keep_admin_required,omitempty"`
	KeepAdminConsented bool          `json:"keep_admin_consented,omitempty"`
	SignIns            []string      `json:"sign_ins"`
	Choices            []MergeChoice `json:"choices"`
	PlanDigest         string        `json:"plan_digest"`
}

// MemberMergeConfirmParams are the params of member.merge.confirm.
// KeepSettingsOf is the id of the member whose settings prevail, the
// survivor. DiscardAck must be true: the caller has seen what is
// discarded.
type MemberMergeConfirmParams struct {
	Code           string `json:"code"`
	KeepSettingsOf string `json:"keep_settings_of"`
	PlanDigest     string `json:"plan_digest"`
	DiscardAck     bool   `json:"discard_ack"`
	KeepAdmin      bool   `json:"keep_admin,omitempty"`
}

// MemberMerge is the audit record of one merge. Discarded holds the
// absorbed member's settings and Absorbed its member row as they were;
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

// MemberMergeConfirmResult is the result of member.merge.confirm: the
// merge and the merged member.
type MemberMergeConfirmResult struct {
	Merge  MemberMerge `json:"merge"`
	Member Member      `json:"member"`
}

// MemberMergeListResult is the result of member.merge.list, newest first.
type MemberMergeListResult struct {
	Merges []MemberMerge `json:"merges"`
}
