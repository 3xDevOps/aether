import { ApiError, type Api } from '@/lib/api'
import type { ViewAttachment, ViewContext, ViewEntry } from '@/lib/assistant-types'
import { isRunView, type RunView } from '@/routes/run/views'
import type { RootState, RootStore } from '@/store'
import type { SliceCreator } from '@/store/slice'

export const maxRecent = 8
export const maxAttachments = 6
export const maxViewContextBytes = 4096
export const maxViewPathBytes = 512
export const maxViewCheckBytes = 128
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
  // A retained attempt holds the context of the old choice; the chips show the new one.
  setAssistantShareChoice: (assistantShareChoice) => set(assistantShareChoice === null ? { assistantShareChoice } : { assistantShareChoice, assistantAttempt: null }),
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
/** The size Go's json.Marshal gives `value`: it writes <, >, &, U+2028 and U+2029 as six-byte \u escapes. */
const marshalledBytes = (value: unknown) => bytes(JSON.stringify(value).replace(/[<>&\u2028\u2029]/g, '\\u0000'))

/** Notes every page the member visits until the returned stop, which forgets them. */
export function trackViewContext(store: RootStore): () => void {
  store.getState().noteView(currentEntry(store.getState()), new Date())
  const stop = store.subscribe((s, prior) => {
    if (s.route !== prior.route || s.shownRunView !== prior.shownRunView) s.noteView(currentEntry(s), new Date())
    // While the member stays on the page a draft was started on, the pin follows its view and open file.
    const pin = s.assistantPin
    const changed = s.route !== prior.route || s.shownRunView !== prior.shownRunView || s.activeFileKey !== prior.activeFileKey || s.fileTabs !== prior.fileTabs
    if (pin && changed) {
      const page = pageContext(s)
      if (sameEntity(pin.page.route, page.route)) store.setState({ assistantPin: { ...pin, page } })
    }
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

type PageContext = Pick<ViewContext, 'open_file' | 'recent'> & { route: ViewEntry }

function pageContext(s: RootState): PageContext {
  const route = currentEntry(s)
  return { route, open_file: openFile(s, route), recent: s.viewHistory.filter((entry) => !sameEntity(entry, route)) }
}

/** Trims as the server does: the oldest recent pages, then the open file, then the last attachments. */
function fit(context: ViewContext): ViewContext {
  while (marshalledBytes(context) > maxViewContextBytes) {
    if (context.recent?.length) context.recent = context.recent.slice(0, -1)
    else if (context.open_file) context.open_file = undefined
    else if (context.attachments?.length) context.attachments = context.attachments.slice(0, -1)
    else break
  }
  return context
}

/** An attachment as the panel holds it, with a label for when the store does not know its target. The label is never sent. */
export type AttachedTarget = ViewAttachment & { label: string }

/** The page a draft was started on. */
export interface DraftPin {
  captured_at: string
  page: PageContext
  /** The page the member moved to, then chose to keep asking about the pinned one instead. */
  keptOver?: string
}

export function pinPage(s: RootState, now = new Date()): DraftPin {
  return { captured_at: now.toISOString(), page: pageContext(s) }
}

export function entityKey(entry: ViewEntry): string {
  return JSON.stringify([entry.name, entry.run_id, entry.mission_id, entry.subagent, entry.run_id ? undefined : entry.workspace_id])
}

/**
 * The page the draft asks about: the pinned one, or the current one while
 * the member is still on it. `moved` is the page the member moved to since,
 * until they choose between the two.
 */
export function draftPage(s: RootState): { page: PageContext; moved?: ViewEntry } {
  const pin = s.assistantPin
  const current = pageContext(s)
  if (!pin || sameEntity(pin.page.route, current.route)) return { page: current }
  return pin.keptOver === entityKey(current.route) ? { page: pin.page } : { page: pin.page, moved: current.route }
}

/** The page the member moved to since starting the draft, while sharing it and still undecided. */
export function draftConflict(s: RootState): ViewEntry | undefined {
  return assistantShareView(s) && !s.assistantPageRemoved ? draftPage(s).moved : undefined
}

const unprintable = /[\p{Cc}\p{Cf}]/u

/** The attachment without its label, and without a check name the server would drop. */
export function sendableAttachment({ label: _label, check, ...attachment }: AttachedTarget): ViewAttachment {
  return check === undefined || bytes(check) > maxViewCheckBytes || unprintable.test(check) ? attachment : { ...attachment, check: check.trim() }
}

/** Exactly what the next prompt carries: the page unless removed or unshared, and the attachments, which sharing does not gate. */
export function draftViewContext(s: RootState): ViewContext | undefined {
  const attachments = s.assistantAttachments.map(sendableAttachment)
  const page = assistantShareView(s) && !s.assistantPageRemoved ? draftPage(s).page : undefined
  if (!page && !attachments.length) return undefined
  return fit({ version: 1, captured_at: s.assistantPin?.captured_at ?? new Date().toISOString(), ...page, ...(attachments.length ? { attachments } : {}) })
}

/** What a prompt retried with sharing now off may still carry. */
export function unsharedPart(context: ViewContext | undefined): ViewContext | undefined {
  return context?.attachments?.length ? { version: 1, captured_at: context.captured_at, attachments: context.attachments } : undefined
}

/** The view context the next prompt sends: a retried prompt's own, else the draft's. */
export function nextViewContext(s: RootState): ViewContext | undefined {
  const retained = s.assistantAttempt?.text === s.assistantDraft.trim() ? s.assistantAttempt : null
  if (!retained) return draftViewContext(s)
  return assistantShareView(s) ? retained.viewContext : unsharedPart(retained.viewContext)
}

const attachmentFields = ['kind', 'workspace_id', 'run_id', 'mission_id', 'task_id', 'attempt_id', 'pr', 'repository', 'check', 'failure', 'seq'] as const

export function sameAttachment(a: ViewAttachment, b: ViewAttachment): boolean {
  return attachmentFields.every((field) => a[field] === b[field])
}

/** The context minus its timestamps, to tell whether anything the assistant would see changed. */
export function viewContextKey(context: ViewContext): string {
  return JSON.stringify({ ...context, captured_at: undefined, recent: context.recent?.map((entry) => ({ ...entry, at: undefined })) })
}

/**
 * The page the agent may read between prompts, matching the chips: nothing
 * while the member removed the page from the draft, the pinned page while
 * they chose it over the one they moved to, and undefined (send nothing new)
 * while they have not chosen yet.
 */
export function syncedViewContext(s: RootState, now = new Date()): ViewContext | undefined {
  if (s.assistantPageRemoved) return { version: 1, captured_at: now.toISOString() }
  const { page, moved } = draftPage(s)
  return moved ? undefined : fit({ version: 1, captured_at: now.toISOString(), ...page })
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
    const context = syncedViewContext(s)
    if (!context) return
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
