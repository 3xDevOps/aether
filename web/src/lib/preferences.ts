import type { Api } from '@/lib/api'
import type { Preference } from '@/lib/types'

/** The row a restore has already added, so a retry after a failed second step only finishes the disable. */
export interface Restore {
  added?: string
}

/**
 * Puts a forgotten preference back as it was, including being turned off.
 * Refuses when the same text already applies to the same scope.
 */
export async function restorePreference(client: Api, gone: Pick<Preference, 'text' | 'workspace_id' | 'enabled'>, progress: Restore = {}): Promise<Preference | undefined> {
  if (!progress.added) {
    const { preferences } = await client.preferenceList({ include_disabled: true })
    if (preferences.some((p) => p.text === gone.text && p.workspace_id === gone.workspace_id)) throw new Error('This preference is already remembered.')
    const added = await client.preferenceAdd({ text: gone.text, ...(gone.workspace_id ? { workspace_id: gone.workspace_id } : {}) })
    progress.added = added.preference.id
    if (gone.enabled) return added.preference
  }
  return (await client.preferenceUpdate({ id: progress.added, enabled: false })).preference
}
