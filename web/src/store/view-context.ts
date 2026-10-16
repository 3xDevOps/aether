import { ApiError, type Api } from '@/lib/api'
import type { ViewContext, ViewEntry } from '@/lib/assistant-types'
import { isRunView, type RunView } from '@/routes/run/views'
import type { RootState, RootStore } from '@/store'
import type { SliceCreator } from '@/store/slice'

export const maxRecent = 8
export const maxViewContextBytes = 4096
export const maxViewPathBytes = 512
/** Navigation sends at most one view context per second. */
export const viewSyncDelay = 1000
export const shareRetryDelay = 3000
const maxRetryDelay = 60_000

/** The server refused the call for good (unavailable, invalid params): retrying cannot help. */
function permanent(err: unknown): boolean {
  return err instanceof ApiError && (err.code === -32004 || err.code === -32602)
}

function backoff(base: number, failures: number): number {
  return Math.min(base * 2 ** failures, maxRetryDelay)
}

export interface ViewContextSlice {
  /** The last `maxRecent` pages visited, newest first, one per entity. Never persisted. */
  viewHistory: ViewEntry[]
  /** The view the run page shows, after remembered tabs and what the run offers. */
  shownRunView: { runId: string; view: RunView } | null
  /** The member's sharing choice until the server has saved it. */
  assistantShareChoice: boolean | null
  noteView: (entry: ViewEntry, at: Date) => void
  clearViewHistory: () => void
  setShownRunView: (runId: string, view: RunView) => void
  setAssistantShareChoice: (choice: boolean | null) => void
}

export const createViewContextSlice: SliceCreator<ViewContextSlice> = (set) => ({
  viewHistory: [],
  shownRunView: null,
  assistantShareChoice: null,
  noteView: (entry, at) =>
    set((s) => ({ viewHistory: [{ ...entry, at: at.toISOString() }, ...s.viewHistory.filter((held) => !sameEntity(held, entry))].slice(0, maxRecent) })),
  clearViewHistory: () => set({ viewHistory: [] }),
  setShownRunView: (runId, view) => set((s) => (s.shownRunView?.runId === runId && s.shownRunView.view === view ? s : { shownRunView: { runId, view } })),
  setAssistantShareChoice: (assistantShareChoice) => set({ assistantShareChoice }),
})

/** Whether prompts carry a view context: the member's pending choice, else the server's setting. */
export function assistantShareView(s: RootState): boolean {
  return s.assistantShareChoice ?? s.assistantStatus?.share_view === true
}

function runView(s: RootState, runId: string): string | undefined {
  if (s.shownRunView?.runId === runId) return s.shownRunView.view
  const asked = s.route.params.view
  return isRunView(asked) ? asked : s.runViewMemory[runId]
}

function currentEntry(s: RootState): ViewEntry {
  const { runId, workspaceId, missionId, agent, view } = s.route.params
  const run = runId ? s.runs[runId] : undefined
  const entry: ViewEntry = { name: s.route.name }
  const workspace = workspaceId ?? run?.workspace_id ?? s.activeWorkspace
  if (workspace) entry.workspace_id = workspace
  if (runId) entry.run_id = runId
  const mission = missionId ?? run?.mission_id
  if (mission) entry.mission_id = mission
  if (agent) entry.subagent = agent
  const shown = runId ? runView(s, runId) : view
  if (shown) entry.view = shown
  return entry
}

function sameEntity(a: ViewEntry, b: ViewEntry): boolean {
  return a.name === b.name && a.run_id === b.run_id && a.mission_id === b.mission_id && a.subagent === b.subagent &&
    (a.run_id !== undefined || a.workspace_id === b.workspace_id)
}

const encoder = new TextEncoder()
const bytes = (text: string) => encoder.encode(text).length

/** Notes every page the member visits until the returned stop, which forgets them. */
export function trackViewContext(store: RootStore): () => void {
  store.getState().noteView(currentEntry(store.getState()), new Date())
  const stop = store.subscribe((s, prior) => {
    if (s.route !== prior.route || s.shownRunView !== prior.shownRunView) s.noteView(currentEntry(s), new Date())
  })
  return () => {
    stop()
    store.getState().clearViewHistory()
  }
}

/** The active file, only while the page shows it: the Files page, or the Files view of its own run. */
function openFile(s: RootState, route: ViewEntry): ViewContext['open_file'] {
  const tab = s.fileTabs.find((t) => t.key === s.activeFileKey)
  if (!tab || bytes(tab.path) > maxViewPathBytes) return undefined
  if (tab.kind === 'home') return route.name === 'files' ? { kind: 'home', path: tab.path } : undefined
  const shown = route.name === 'files' || (route.name === 'run' && route.view === 'files' && route.run_id === tab.runID)
  return shown ? { kind: 'workspace', path: tab.path, run_id: tab.runID || undefined, workspace_id: tab.workspaceID || undefined } : undefined
}

/** What the assistant is told about the member's dashboard: ids and view names, never titles, contents, drafts or terminal text. */
export function buildViewContext(s: RootState, now = new Date()): ViewContext {
  const route = currentEntry(s)
  const context: ViewContext = {
    version: 1,
    captured_at: now.toISOString(),
    route,
    open_file: openFile(s, route),
    recent: s.viewHistory.filter((entry) => !sameEntity(entry, route)),
  }
  while (context.recent?.length && bytes(JSON.stringify(context)) > maxViewContextBytes) {
    context.recent = context.recent.slice(0, -1)
  }
  return context
}

/** The context minus its timestamps, to tell whether anything the assistant would see changed. */
export function viewContextKey(context: ViewContext): string {
  return JSON.stringify({ ...context, captured_at: undefined, recent: context.recent?.map((entry) => ({ ...entry, at: undefined })) })
}

/** While it runs, sends the server each changed view context, one at a time and debounced, retrying a failed one with backoff. */
export function syncViewContext(store: RootStore, client: Pick<Api, 'assistantView'>): () => void {
  let sent = ''
  let refused = ''
  let failures = 0
  let sending = false
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const schedule = (delay = viewSyncDelay) => {
    if (!stopped && timer === undefined) timer = setTimeout(flush, delay)
  }
  const flush = () => {
    timer = undefined
    const s = store.getState()
    if (sending || !assistantShareView(s)) return
    const context = buildViewContext(s)
    const key = viewContextKey(context)
    if (key === sent || key === refused) return
    sending = true
    client.assistantView(context).then(() => {
      sent = key
      failures = 0
    }, (err: unknown) => {
      if (permanent(err)) refused = key
      else failures++
    }).finally(() => {
      sending = false
      clearTimeout(timer)
      timer = undefined
      schedule(backoff(viewSyncDelay, failures))
    })
  }
  schedule()
  const stop = store.subscribe((s, prior) => {
    // A prompt may leave its own, older context on the server; send the current one again.
    if (prior.assistantSending && !s.assistantSending) sent = ''
    schedule()
  })
  return () => {
    stopped = true
    stop()
    if (timer !== undefined) clearTimeout(timer)
  }
}

const shareSaves = new WeakMap<RootStore, { last: Promise<unknown>; retry?: ReturnType<typeof setTimeout> }>()

/**
 * Saves the member's sharing choice on the server, retrying with backoff
 * until it lands, the server refuses it for good, or the member chooses
 * again. Saves go one at a time, so an older choice cannot land after a
 * newer one, and one is sent only while it is still the signed-in member's
 * current choice.
 */
export function saveShareView(store: RootStore, client: Pick<Api, 'assistantSettings'>, on: boolean, onError: (err: unknown) => void) {
  const saves = shareSaves.get(store) ?? { last: Promise.resolve() }
  shareSaves.set(store, saves)
  clearTimeout(saves.retry)
  const identity = store.getState().identityKey
  const current = () => {
    const s = store.getState()
    return s.identityKey === identity && s.assistantShareChoice === on
  }
  store.getState().setAssistantShareChoice(on)
  const attempt = (failures: number) => {
    saves.last = saves.last.then(() => current() ? client.assistantSettings({ share_view: on }).then((settings) => {
      if (!current()) return
      const s = store.getState()
      s.setAssistantShareChoice(null)
      if (s.assistantStatus) s.setAssistantStatus({ ...s.assistantStatus, ...settings })
    }, (err: unknown) => {
      if (!current()) return
      onError(err)
      if (permanent(err)) store.getState().setAssistantShareChoice(null)
      else saves.retry = setTimeout(() => attempt(failures + 1), backoff(shareRetryDelay, failures))
    }) : undefined)
  }
  attempt(0)
}
