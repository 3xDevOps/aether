import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Invitation, Member, MemberList, MergeProposal } from '@/lib/types'
import type { RootState, RootStore } from '@/store'
import { byCreated } from '@/store/collaboration'
import { capability } from '@/store/hooks'
import type { SliceCreator } from '@/store/slice'

/** A provisional invitation is one the dashboard shows before the server has assigned it an id. */
export type ListedInvitation = Invitation & { provisional?: boolean }

/** One pending change made by `patchMember`; pass it back to `confirmMember` or `revertMember`. */
export interface MemberPatch {
  applied: Partial<Member>
  /** From `nextMemberOrder` when the change was started. */
  order: number
  lineage?: Lineage
}

interface Lineage {
  base: Member
  confirmed: Partial<Member>
  pending: MemberPatch[]
}

export interface MembersSlice {
  members: Record<string, Member>
  /** From `member.list`: each member a merge absorbed, to the member it became. */
  mergedInto: Record<string, string>
  /** Without `mergedInto`, the map already held stays. */
  setMembers: (members: Member[], mergedInto?: Record<string, string>) => void
  setMemberList: (list: MemberList) => void
  /** Returns false, writing nothing, when an answer to a later-started change already wrote these fields. */
  confirmMember: (id: string, fields: Partial<Member>, patch?: MemberPatch) => boolean
  /** Pass the same `order` to every retry of one change. */
  patchMember: (id: string, patch: Partial<Member>, order?: number) => MemberPatch
  /** Call with the read as it is sent, not once it has answered. */
  readMembers: (read: Promise<MemberList>) => Promise<MemberList>
  revertMember: (id: string, patch?: MemberPatch) => void
  invitations: ListedInvitation[] | null
  /** Ids of invitations whose revoke awaits the server. */
  revokingInvitations: string[]
  /** `since` is the list as it stood when the read was sent. */
  setInvitations: (invitations: Invitation[], since: ListedInvitation[] | null) => void
  /** Call with the read as it is sent, not once it has answered. */
  readInvitations: (read: Promise<Invitation[]>) => Promise<Invitation[]>
  addInvitation: (invitation: ListedInvitation, replaces?: string) => void
  dropInvitation: (id: string) => void
  setRevokingInvitation: (id: string, pending: boolean) => void
  /** From `member.merge.pending`: open proposals the viewer made or is asked to accept. */
  mergeProposals: MergeProposal[]
  /** The last `member.merge.pending` read's failure; the proposals shown are from before it. */
  mergeProposalsError: string | null
  setMergeProposals: (proposals: MergeProposal[], error?: string | null) => void
  /** When the merge code this tab minted expires, in ms; proposals are polled until then. */
  mergeCodeUntil: number
  setMergeCodeUntil: (until: number) => void
}

function withMember(s: RootState, id: string, patch: Partial<Member>): Partial<RootState> {
  const member = s.members[id]
  return {
    ...(member && { members: { ...s.members, [id]: { ...member, ...patch } } }),
    ...(s.info?.member.id === id && { info: { ...s.info, member: { ...s.info.member, ...patch } } }),
  }
}

const memberOf = (s: RootState, id: string) => s.members[id] ?? (s.info?.member.id === id ? s.info.member : undefined)

/** Maps each record a prediction wrote to its lineage; server records are absent. */
const lineages = new WeakMap<Member, Lineage>()

function predict(s: RootState, id: string, patch: Partial<Member>, lineage: Lineage): Partial<RootState> {
  const next = withMember(s, id, patch)
  const written = next.members?.[id] ?? next.info?.member
  if (written) lineages.set(written, lineage)
  return next
}

let orders = 0
export const nextMemberOrder = () => ++orders

/** Per member and field, the order of the change whose answer was written last. */
const written = new Map<string, number>()
/** Per `member.list` read in flight, the members changed since it was sent. */
const reads = new Set<Set<string>>()
const changed = (id: string) => reads.forEach((read) => read.add(id))
/** Per `member.invitation.list` read in flight, the invitations removed since it was sent. */
const invitationReads = new Set<Set<string>>()

const settle = (patch?: MemberPatch) => {
  if (patch?.lineage) patch.lineage.pending = patch.lineage.pending.filter((p) => p !== patch)
}

export const createMembersSlice: SliceCreator<MembersSlice> = (set, get) => ({
  members: {},
  mergedInto: {},
  setMembers: (members, mergedInto) =>
    set({ members: Object.fromEntries(members.map((m) => [m.id, m])), ...(mergedInto && { mergedInto }) }),
  setMemberList: (list) => get().setMembers(list.members, list.merged_into ?? {}),
  confirmMember: (id, fields, patch) => {
    settle(patch)
    changed(id)
    if (patch) {
      const keys = Object.keys(fields).map((key) => `${id}\u0000${key}`)
      if (keys.some((key) => (written.get(key) ?? 0) > patch.order)) return false
      keys.forEach((key) => written.set(key, patch.order))
    }
    set((s) => {
      const current = memberOf(s, id)
      const lineage = current && lineages.get(current)
      if (!lineage) return withMember(s, id, fields)
      Object.assign(lineage.confirmed, fields)
      return predict(s, id, fields, lineage)
    })
    return true
  },
  patchMember: (id, applied, order = nextMemberOrder()) => {
    changed(id)
    const current = memberOf(get(), id)
    const lineage = current && (lineages.get(current) ?? { base: current, confirmed: {}, pending: [] })
    const patch = { applied, order, lineage }
    if (lineage) {
      lineage.pending.push(patch)
      set((s) => predict(s, id, applied, lineage))
    }
    return patch
  },
  revertMember: (id, patch) => {
    settle(patch)
    const lineage = patch?.lineage
    if (!lineage) return
    set((s) => {
      const current = memberOf(s, id)
      if (!current) return {}
      if (lineages.get(current) !== lineage) {
        const shown = s.members[id]
        return shown && s.info?.member.id === id && s.info.member !== shown ? { info: { ...s.info, member: { ...s.info.member, ...shown } } } : {}
      }
      const keys = (Object.keys(patch.applied) as (keyof Member)[]).filter((key) => current[key] === patch.applied[key])
      const back = keys.map((key) => {
        const newer = [...lineage.pending].reverse().find((p) => key in p.applied)
        return [key, newer ? newer.applied[key] : key in lineage.confirmed ? lineage.confirmed[key] : lineage.base[key]]
      })
      return predict(s, id, Object.fromEntries(back), lineage)
    })
  },
  readMembers: async (read) => {
    const since = new Set<string>()
    reads.add(since)
    try {
      const list = await read
      return { merged_into: list.merged_into ?? {}, members: list.members.map((m) => {
        const shown = get().members[m.id]
        if (!shown) return m
        if (since.has(m.id)) return shown
        const lineage = lineages.get(shown)
        if (!lineage?.pending.length) return m
        const pending = lineage.pending.flatMap((p) => Object.keys(p.applied) as (keyof Member)[])
        const kept = { ...m, ...Object.fromEntries(pending.map((key) => [key, shown[key]])) }
        lineages.set(kept, lineage)
        return kept
      }) }
    } finally {
      reads.delete(since)
    }
  },
  invitations: null,
  revokingInvitations: [],
  setInvitations: (invitations, since) =>
    set((s) => {
      const was = new Set((since ?? []).map((i) => i.id))
      const now = s.invitations ?? []
      const added = now.filter((i) => i.provisional || !was.has(i.id))
      const gone = new Set([...was].filter((id) => !now.some((i) => i.id === id)))
      const read = invitations.filter((i) => !gone.has(i.id) && !s.revokingInvitations.includes(i.id) && !added.some((a) => a.id === i.id))
      return { invitations: byCreated([...read, ...added]) }
    }),
  readInvitations: async (read) => {
    const removed = new Set<string>()
    invitationReads.add(removed)
    try {
      return (await read).filter((i) => !removed.has(i.id))
    } finally {
      invitationReads.delete(removed)
    }
  },
  addInvitation: (invitation, replaces) => {
    invitationReads.forEach((removed) => removed.delete(invitation.id))
    set((s) => ({
      invitations: byCreated([...(s.invitations ?? []).filter((i) => i.id !== invitation.id && i.id !== replaces), invitation]),
    }))
  },
  dropInvitation: (id) => {
    invitationReads.forEach((removed) => removed.add(id))
    set((s) => ({ invitations: s.invitations && s.invitations.filter((i) => i.id !== id) }))
  },
  setRevokingInvitation: (id, pending) =>
    set((s) => ({ revokingInvitations: [...s.revokingInvitations.filter((r) => r !== id), ...(pending ? [id] : [])] })),
  mergeProposals: [],
  mergeProposalsError: null,
  setMergeProposals: (mergeProposals, error = null) => set({ mergeProposals, mergeProposalsError: error }),
  mergeCodeUntil: 0,
  setMergeCodeUntil: (mergeCodeUntil) => set({ mergeCodeUntil }),
})

const proposalReads = new WeakMap<RootStore, number>()
const nextProposalRead = (store: RootStore) => {
  const read = (proposalReads.get(store) ?? 0) + 1
  proposalReads.set(store, read)
  return read
}

/** Reads `member.merge.pending`. Only the latest read lands, a removal made
 * since a read started outdates it, and a failure lands as
 * `mergeProposalsError` beside the proposals already shown. */
export async function loadMergeProposals(store: RootStore, client: Api): Promise<void> {
  const read = nextProposalRead(store)
  const s = store.getState()
  if (!s.info || !capability(s.capabilities).hasMethod('member.merge.pending')) return s.setMergeProposals([])
  let proposals: MergeProposal[] | undefined
  let error: string | null = null
  try {
    proposals = await client.memberMergePending()
  } catch (err) {
    error = message(err)
  }
  const now = store.getState()
  if (proposalReads.get(store) !== read || now.identityKey !== s.identityKey) return
  now.setMergeProposals(proposals ?? now.mergeProposals, error)
}

export function dropMergeProposal(store: RootStore, id: string) {
  nextProposalRead(store)
  const s = store.getState()
  s.setMergeProposals(s.mergeProposals.filter((p) => p.id !== id), s.mergeProposalsError)
}
