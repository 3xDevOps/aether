import type { RootState } from '@/store'

const maxHints = 200
const maxHintBytes = 80

/** `text` in place of the selection, spaced from the words around it; the caret lands after it. */
export function insertTranscript(body: string, text: string, start: number, end: number): { body: string; caret: number } {
  const before = body.slice(0, Math.min(start, body.length))
  const after = body.slice(Math.min(end, body.length))
  const lead = before && !/\s$/.test(before) ? ' ' : ''
  const trail = after && !/^\s/.test(after) ? ' ' : ''
  return { body: before + lead + text + trail + after, caret: before.length + lead.length + text.length }
}

/** Names the speaker may say that a speech model would not know: workspaces, repos and the open workspace's sessions, newest first. */
export function dictationHints(state: Pick<RootState, 'workspaces' | 'runs' | 'route' | 'activeWorkspace'>): string[] {
  const routed = state.route.params.runId ? state.runs[state.route.params.runId]?.workspace_id : state.route.params.workspaceId
  const workspaceID = routed ?? state.activeWorkspace
  const runs = Object.values(state.runs)
    .filter((run) => run.workspace_id === workspaceID && !run.archived_at)
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
  const origin = state.workspaces[workspaceID]?.origin?.replace(/\.git$/, '').split(/[/:]/).pop()
  const names = [
    ...Object.values(state.workspaces).map((workspace) => workspace.name),
    origin ?? '',
    ...runs.flatMap((run) => (run.repos ?? []).map((repo) => repo.name)),
    ...runs.map((run) => run.child_purpose ?? run.title ?? ''),
  ]
  const encoder = new TextEncoder()
  const fits = (name: string) => name !== '' && encoder.encode(name).length <= maxHintBytes
  return [...new Set(names.map((name) => name.trim()).filter(fits))].slice(0, maxHints)
}
