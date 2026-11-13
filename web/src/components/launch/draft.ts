import type { Fallback, ChildRunsChoice } from '@/components/launch/launch-options'
import type { LimitsDraft } from '@/components/launch/run-limits'
import type { LaunchMode, Permission } from '@/lib/types'
import { message } from '@/lib/format'

export interface LaunchDraft {
  version: 1
  id: string
  workspace: string
  extras: string[]
  account: string
  fallback: Fallback
  permission: Permission
  childRuns: ChildRunsChoice
  limits: LimitsDraft
  harness: string
  mode: LaunchMode
  model: string
  effort: string
  task: string
}

const storageKey = (identity: string) => `aether.launch-draft.v1:${encodeURIComponent(identity)}`
const strings = (value: unknown): value is string[] => Array.isArray(value) && value.every((item) => typeof item === 'string')

export function readLaunchDraft(identity: string): LaunchDraft | null {
  try {
    const draft = JSON.parse(localStorage.getItem(storageKey(identity)) ?? 'null') as LaunchDraft | null
    if (!draft || draft.version !== 1 || !draft.id ||
      !['id', 'workspace', 'account', 'harness', 'model', 'effort', 'task'].every((key) => typeof draft[key as keyof LaunchDraft] === 'string') ||
      !strings(draft.extras) || !draft.fallback || typeof draft.fallback.off !== 'boolean' ||
      !(draft.fallback.order === null || strings(draft.fallback.order)) || !strings(draft.fallback.excluded) ||
      !['root', 'default', 'isolated'].includes(draft.permission) || !['', 'on', 'off'].includes(draft.childRuns) ||
      !['tui', 'acp', 'headless'].includes(draft.mode) || !draft.limits ||
      !['cpus', 'memory', 'pids'].every((key) => typeof draft.limits[key as keyof LimitsDraft] === 'string')) return null
    return draft
  } catch { return null }
}

export function saveLaunchDraft(identity: string | null, draft: LaunchDraft | null): string | null {
  if (!identity) return null
  try {
    if (draft) localStorage.setItem(storageKey(identity), JSON.stringify(draft))
    else localStorage.removeItem(storageKey(identity))
    return null
  } catch (error) { return message(error) }
}
