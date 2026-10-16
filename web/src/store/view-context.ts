import type { Api } from '@/lib/api'
import type { ViewContext, ViewEntry } from '@/lib/assistant-types'
import type { RootState, RootStore } from '@/store'
import { pendingApprovals } from '@/store/approvals'
import { runRows, stateContextOf } from '@/store/selectors'
import type { Route } from '@/store/ui'

export const maxRecent = 8
export const maxViewContextBytes = 4096
const maxNeedsYou = 5
/** The server keeps one view context per member; navigation sends at most one per second. */
export const viewSyncDelay = 1000

function entryOf(route: Route, s: Pick<RootState, 'runs' | 'activeWorkspace'>): ViewEntry {
  const { runId, workspaceId, missionId, agent, view } = route.params
  const run = runId ? s.runs[runId] : undefined
  const entry: ViewEntry = { name: route.name }
  const workspace = workspaceId ?? run?.workspace_id ?? s.activeWorkspace
  if (workspace) entry.workspace_id = workspace
  if (runId) entry.run_id = runId
  const mission = missionId ?? run?.mission_id
  if (mission) entry.mission_id = mission
  if (agent) entry.subagent = agent
  if (view) entry.view = view
  return entry
}

function sameEntity(a: ViewEntry, b: ViewEntry): boolean {
  return a.name === b.name && a.run_id === b.run_id && a.mission_id === b.mission_id && a.subagent === b.subagent &&
    (a.run_id !== undefined || a.workspace_id === b.workspace_id)
}

/** The last `maxRecent` pages visited, newest first, one per entity. Lives only as long as the page. */
export function createViewHistory() {
  let recent: ViewEntry[] = []
  return {
    note(entry: ViewEntry, at: Date) {
      recent = [{ ...entry, at: at.toISOString() }, ...recent.filter((held) => !sameEntity(held, entry))].slice(0, maxRecent)
    },
    list: (): readonly ViewEntry[] => recent,
    clear() {
      recent = []
    },
  }
}

export type ViewHistory = ReturnType<typeof createViewHistory>

export const viewHistory = createViewHistory()

/** Notes every page the member visits until the returned stop, which forgets them. */
export function trackViewContext(store: RootStore, history: ViewHistory = viewHistory): () => void {
  history.note(entryOf(store.getState().route, store.getState()), new Date())
  const stop = store.subscribe((s, prior) => {
    if (s.route !== prior.route) history.note(entryOf(s.route, s), new Date())
  })
  return () => {
    stop()
    history.clear()
  }
}

function openFile(s: RootState): ViewContext['open_file'] {
  const tab = s.fileTabs.find((t) => t.key === s.activeFileKey)
  if (!tab) return undefined
  if (tab.kind === 'home') return { kind: 'home', path: tab.path }
  return { kind: 'workspace', path: tab.path, run_id: tab.runID || undefined, workspace_id: tab.workspaceID || undefined }
}

/** What the assistant is told about the member's dashboard: ids, view names and counts, never titles, contents, drafts or terminal text. */
export function buildViewContext(s: RootState, history: ViewHistory = viewHistory, now = new Date()): ViewContext {
  const route = entryOf(s.route, s)
  const runID = s.route.params.runId
  const viewer = s.info?.member.id
  const needsYou = viewer
    ? runRows(stateContextOf(s, now.getTime()))
      .filter((row) => row.state === 'needs-you' && row.run.member_id === viewer)
      .slice(0, maxNeedsYou)
      .map((row) => ({ run_id: row.run.id, ...(row.needsYou ? { reason: row.needsYou } : {}) }))
    : []
  const context: ViewContext = {
    version: 1,
    captured_at: now.toISOString(),
    route,
    open_file: openFile(s),
    layout: {
      sidebar_collapsed: s.sidebarCollapsed,
      details_open: s.detailsOpen,
      browser_beside: s.browserBeside,
      ...(runID ? { changes_bar_open: s.changesOpen[runID] === true } : {}),
    },
    recent: history.list().filter((entry) => !sameEntity(entry, route)),
    pending: {
      approvals: pendingApprovals(s.inbox).length,
      assistant_notices: s.assistantNotices,
      ...(needsYou.length ? { needs_you: needsYou } : {}),
    },
  }
  while (context.recent?.length && JSON.stringify(context).length > maxViewContextBytes) {
    context.recent = context.recent.slice(0, -1)
  }
  return context
}

/** The context minus its timestamps, to tell whether anything the assistant would see changed. */
function contentKey(context: ViewContext): string {
  return JSON.stringify({ ...context, captured_at: undefined, recent: context.recent?.map((entry) => ({ ...entry, at: undefined })) })
}

/** While it runs, sends the server each changed view context, debounced. Sharing turned off stops it. */
export function syncViewContext(store: RootStore, client: Pick<Api, 'assistantView'>, history: ViewHistory = viewHistory): () => void {
  let sent = ''
  let timer: ReturnType<typeof setTimeout> | undefined
  const flush = () => {
    timer = undefined
    const s = store.getState()
    if (!s.assistantShareView) return
    const context = buildViewContext(s, history)
    const key = contentKey(context)
    if (key === sent) return
    sent = key
    client.assistantView(context).catch(() => {
      sent = ''
    })
  }
  const schedule = () => {
    if (timer === undefined) timer = setTimeout(flush, viewSyncDelay)
  }
  schedule()
  const stop = store.subscribe((s, prior) => {
    if (s.route !== prior.route || s.activeFileKey !== prior.activeFileKey || (s.assistantShareView && !prior.assistantShareView)) {
      if (s.assistantShareView && !prior.assistantShareView) sent = ''
      schedule()
    }
  })
  return () => {
    stop()
    if (timer !== undefined) clearTimeout(timer)
  }
}
