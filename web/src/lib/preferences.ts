import type { Api } from '@/lib/api'
import type { Preference } from '@/lib/types'
import { sameIdentity } from '@/store'

/** The row a restore has already added, so a retry after a failed second step only finishes the disable, and the member it started for. */
export interface Restore {
  added?: string
  same?: () => boolean
}

/**
 * Puts a forgotten preference back as it was, including being turned off.
 * Refuses when the same text already applies to the same scope, or once
 * another member is signed in.
 */
export async function restorePreference(client: Api, gone: Pick<Preference, 'text' | 'workspace_id' | 'enabled'>, progress: Restore = {}): Promise<Preference | undefined> {
  const same = (progress.same ??= sameIdentity())
  const checkMember = () => {
    if (!same()) throw new Error('Another member is signed in now, so this preference was not put back.')
  }
  if (!progress.added) {
    const { preferences } = await client.preferenceList({ include_disabled: true })
    if (preferences.some((p) => p.text === gone.text && p.workspace_id === gone.workspace_id)) throw new Error('This preference is already remembered.')
    checkMember()
    const added = await client.preferenceAdd({ text: gone.text, ...(gone.workspace_id ? { workspace_id: gone.workspace_id } : {}) })
    progress.added = added.preference.id
    if (gone.enabled) return added.preference
  }
  checkMember()
  return (await client.preferenceUpdate({ id: progress.added, enabled: false })).preference
}
