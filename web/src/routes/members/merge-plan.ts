import type { Member, MergeChoice, MergeMember, MergePlan, MergeProposal, MergeSettings } from '@/lib/types'

export interface ProposeForm {
  keep: string | null
  discardAck: boolean
  keepAdmin: boolean
}

export interface MergeOutcome {
  /** The choice the form names; absent until settings are chosen. */
  choice?: MergeChoice
  survivor?: MergeMember
  absorbed?: MergeMember
  /** The merged member's role as the plan stands with this form. */
  role: Member['role']
  /** Whether this side may keep its own admin or super admin role for the merged member. */
  keepAdminOffered: boolean
  canSubmit: boolean
}

const memberOf = (plan: MergePlan, id: string) => (plan.minted.id === id ? plan.minted : plan.proposer.id === id ? plan.proposer : undefined)

const roleRank: Record<Member['role'], number> = { viewer: 0, collaborator: 1, admin: 2, super_admin: 3 }
const canKeep = (role: Member['role']) => role === 'admin' || role === 'super_admin'

/** The merged role as merge.go computes it: the lower of the two, raised to the role of each admin or super admin side that keeps it. */
function mergedRole(plan: MergePlan, mintedKeeps: boolean, proposerKeeps: boolean): Member['role'] {
  const { minted, proposer } = plan
  let role = roleRank[proposer.role] < roleRank[minted.role] ? proposer.role : minted.role
  for (const [keeps, side] of [[mintedKeeps, minted], [proposerKeeps, proposer]] as const) {
    if (keeps && canKeep(side.role) && roleRank[side.role] > roleRank[role]) role = side.role
  }
  return role
}

function outcome(plan: MergePlan, keep: string | null, side: MergeMember, keepAdmin: boolean, otherKeepsAdmin: boolean): Omit<MergeOutcome, 'canSubmit'> {
  const choice = keep ? plan.choices.find((c) => c.keep_settings_of === keep) : undefined
  const keepAdminOffered = Boolean(plan.role_with_keep_admin) && canKeep(side.role)
  const sideKeeps = keepAdminOffered && keepAdmin
  const sideIsMinted = side.id === plan.minted.id
  return {
    choice,
    survivor: choice && memberOf(plan, choice.keep_settings_of),
    absorbed: choice && memberOf(plan, choice.absorbed_id),
    role: mergedRole(plan, sideIsMinted ? sideKeeps : otherKeepsAdmin, sideIsMinted ? otherKeepsAdmin : sideKeeps),
    keepAdminOffered,
  }
}

/** The proposer's form: settings must be chosen and the discard acknowledged. */
export function proposeOutcome(plan: MergePlan, form: ProposeForm): MergeOutcome {
  const shown = outcome(plan, form.keep, plan.proposer, form.keepAdmin, false)
  return { ...shown, canSubmit: Boolean(shown.choice) && form.discardAck }
}

/** The minter's accept: the proposal fixed the settings; the discard must be acknowledged again. */
export function acceptOutcome(proposal: MergeProposal & { plan: MergePlan }, discardAck: boolean, keepAdmin: boolean): MergeOutcome {
  const { plan } = proposal
  const shown = outcome(plan, proposal.keep_settings_of, plan.minted, keepAdmin, Boolean(proposal.proposer_keep_admin) && canKeep(plan.proposer.role))
  return { ...shown, canSubmit: Boolean(shown.choice) && discardAck && !proposal.refusal }
}

/** What a merge discards from a member's settings, one line each. */
export function settingsLines(settings: MergeSettings, names: { member: (id: string) => string; workspace: (id: string) => string }): string[] {
  return [
    `Display name ${settings.display_name}`,
    `Colour ${settings.color}`,
    settings.git_name && `Git name ${settings.git_name}`,
    settings.git_email && `Git email ${settings.git_email}`,
    settings.image && `Saved environment image ${settings.image}`,
    settings.harness_definitions.length > 0 && `Custom agents: ${settings.harness_definitions.map((h) => h.name).join(', ')}`,
    settings.profile_heads.length > 0 && `Saved agent profiles: ${settings.profile_heads.map((p) => p.harness).join(', ')}`,
    settings.account_shared_with.length > 0 && `Agent account shared with ${settings.account_shared_with.map(names.member).join(', ')}`,
    settings.mirror_workspaces.length > 0 && `GitHub connection used by the mirror of ${settings.mirror_workspaces.map(names.workspace).join(', ')}`,
    settings.home && 'Home on the server (agent logins, files) and git signing key, archived on the server',
  ].filter((line): line is string => Boolean(line))
}
