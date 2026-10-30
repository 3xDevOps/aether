import type { ViewAttachment, ViewEntry } from '@/lib/assistant-types'
import { runLabel } from '@/lib/status'
import { surfaces } from '@/lib/surfaces'
import type { RootState } from '@/store'
import type { AttachedTarget } from '@/store/view-context'

const everything = { hasMethod: () => true, hasLocal: () => true, hasWS: () => true }

export const kindWord: Record<ViewAttachment['kind'] | 'page', string> = {
  page: 'Page',
  workspace: 'Workspace',
  run: 'Run',
  pr: 'PR',
  check: 'Check',
  failure: 'Error',
}

function runName(s: RootState, id: string): string {
  const run = s.runs[id]
  return run ? runLabel(run) : id
}

/** The page as the member knows it: the run's title, else the page's name. */
export function pageLabel(s: RootState, entry: ViewEntry): string {
  if (entry.run_id) return runName(s, entry.run_id)
  return surfaces(everything).find((surface) => surface.name === entry.name)?.label ?? entry.name
}

export function attachmentLabel(s: RootState, target: AttachedTarget): string {
  if (target.kind === 'run' && target.run_id) return runName(s, target.run_id)
  return target.label
}

/** The ids an attachment sends, as one line. */
export function attachmentIds(attachment: ViewAttachment): string {
  return [
    attachment.workspace_id,
    attachment.run_id,
    attachment.repository && attachment.pr ? `${attachment.repository}#${attachment.pr}` : attachment.pr && `#${attachment.pr}`,
    attachment.check && `check ${attachment.check}`,
    attachment.failure && `failure ${attachment.failure}${attachment.seq ? ` at ${attachment.seq}` : ''}`,
  ].filter(Boolean).join(' · ')
}

export function entryIds(entry: ViewEntry): string {
  return [entry.workspace_id, entry.run_id, entry.subagent, entry.view].filter(Boolean).join(' · ')
}
