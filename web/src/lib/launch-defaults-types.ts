import type { LaunchMode } from '@/lib/types'

export type FallbackPolicy = 'automatic' | 'ordered' | 'off'

/** A member's saved launch choices; launch.defaults.get and set. Empty fields keep the server's own defaults. */
export interface SavedLaunchDefaults {
  agent?: string
  agents?: Record<string, { mode?: LaunchMode | ''; model?: string }>
  /** Empty is the member's own account. */
  account_member_id?: string
  fallback?: { policy: FallbackPolicy; order?: string[] }
}

/** `field` is "agent", "agents.<name>", "agents.<name>.mode", "agents.<name>.model", "account_member_id" or "fallback.order". */
export interface LaunchDefaultIssue {
  field: string
  value: string
  reason: string
}

/** Revision 0 means nothing is saved. */
export interface LaunchDefaultsResult {
  revision: number
  defaults: SavedLaunchDefaults
  source?: 'member' | 'assistant'
  updated_at?: string
  invalid?: LaunchDefaultIssue[]
  unknown?: LaunchDefaultIssue[]
}

export interface LaunchDefaultsSetParams {
  defaults?: SavedLaunchDefaults
  clear?: boolean
  revision: number
}
