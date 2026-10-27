import type { Api } from '@/lib/api'
import type { Preference } from '@/lib/types'

/** Puts a forgotten preference back as it was, including being turned off. */
export async function restorePreference(client: Api, gone: Pick<Preference, 'text' | 'workspace_id' | 'enabled'>): Promise<Preference> {
  const added = await client.preferenceAdd({ text: gone.text, ...(gone.workspace_id ? { workspace_id: gone.workspace_id } : {}) })
  if (gone.enabled) return added.preference
  return (await client.preferenceUpdate({ id: added.preference.id, enabled: false })).preference
}
