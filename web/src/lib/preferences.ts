import type { Api } from '@/lib/api'
import type { Preference } from '@/lib/types'
import { sameIdentity, useStore } from '@/store'

/** The row a restore has already added, so a retry after a failed second step only finishes the disable, and the member it started for. */
export interface Restore {
  added?: string
  /** The version the added row had, which the turn-off must still find. */
  addedAt?: string
  same?: () => boolean
  member?: string
}

/**
 * Puts a forgotten preference back as it was, including being turned off.
 * The server refuses a duplicate of the same text and scope, and a write once
 * another member is signed in; this refuses the second step the same way.
 */
export async function restorePreference(client: Api, gone: Pick<Preference, 'text' | 'workspace_id' | 'enabled'>, progress: Restore = {}): Promise<Preference | undefined> {
  const same = (progress.same ??= sameIdentity())
  progress.member ??= useStore.getState().info?.member?.id
  const checkMember = () => {
    if (!same()) throw new Error('Another member is signed in now, so this preference was not put back.')
  }
  if (!progress.added) {
    checkMember()
    const added = await client.preferenceAdd({ text: gone.text, ...(gone.workspace_id ? { workspace_id: gone.workspace_id } : {}), expected_member_id: progress.member })
    progress.added = added.preference.id
    progress.addedAt = added.preference.updated_at
    if (gone.enabled) return added.preference
  }
  checkMember()
  return (await client.preferenceUpdate({ id: progress.added, enabled: false, expected_member_id: progress.member, expected_updated_at: progress.addedAt })).preference
}
