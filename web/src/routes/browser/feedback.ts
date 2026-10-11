import { create } from 'zustand'
import type { DevArtifact, DevBrowserConsoleEntry, DevBrowserElement, DevBrowserNetworkEntry, RoomMessage } from '@/lib/types'
import { fenced } from '@/routes/diff/review'
import { useStore } from '@/store'
import { pruneRuns } from '@/store/runs'

/** The page a note was written on, as its message names it. */
export interface NotePage {
  url: string
  width: number
  height: number
}

interface NoteBase {
  id: string
  page: NotePage
  /** What the person wrote; '' until they confirm it. */
  body: string
  /** The text in an open card; absent once the card is closed. */
  draft?: string
}

export interface ElementNote extends NoteBase {
  kind: 'element'
  /** The tab and scroll position `point` and the element's bounds were read at. */
  at: { page_id: string; scroll_x: number; scroll_y: number }
  /** Where the person pointed, in viewport coordinates. */
  point: { x: number; y: number }
  /** Absent until the page has answered. */
  element?: DevBrowserElement
  /** Why the page did not answer. */
  failure?: string
  /** The element as the frame showed it when it was picked. */
  image?: Blob
  /** Where `image` is inside the run, once uploaded. A retried send reuses it. */
  path?: string
}

export interface ScreenshotNote extends NoteBase {
  kind: 'screenshot'
  /** The capture the server took; its path is readable inside the run. */
  artifact: DevArtifact
}

export type Note = ElementNote | ScreenshotNote

export type LogEntry = (({ kind: 'console' } & DevBrowserConsoleEntry) | ({ kind: 'network' } & DevBrowserNetworkEntry)) & {
  /** The address the page was at when the row was read. */
  page: string
}

/** Everything from one run's Browser that waits to be sent to its agent. */
export interface Feedback {
  notes: Note[]
  /** The console and network rows the person ticked. */
  logs: LogEntry[]
  /** The send in flight. It settles only the feedback that still carries its mark. */
  sending?: number
  /** Why the last send left everything in place. */
  error?: string
  /** The room message that carried the last batch, and what it carried. */
  sent?: { message: RoomMessage; what: string }
}

/** A note is rarely over 2 KiB, so this many stay well inside the room's 64 KiB
 * message. The bar checks the real size before it sends. */
export const maxNotes = 20

const none: Feedback = { notes: [], logs: [] }

// Not a root-store slice, for the reason a diff review is not: a write there
// reaches a controlled textarea a frame late.
const useFeedbacks = create<Record<string, Feedback>>(() => ({}))

export function feedback(runID: string): Feedback {
  return useFeedbacks.getState()[runID] ?? none
}

export function useFeedback(runID: string): Feedback {
  return useFeedbacks((all) => all[runID] ?? none)
}

export function patchFeedback(runID: string, patch: Partial<Feedback>): void {
  const next = { ...feedback(runID), ...patch }
  const empty = next.notes.length === 0 && next.logs.length === 0
  if (empty) next.error = undefined
  const all = { ...useFeedbacks.getState(), [runID]: next }
  if (empty && !next.sending && !next.sent) delete all[runID]
  useFeedbacks.setState(all, true)
}

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useFeedbacks.setState({}, true)
  else if (state.runs !== previous.runs) useFeedbacks.setState(pruneRuns(useFeedbacks.getState(), (runID) => runID in state.runs), true)
})

/** What the note says now: an open card's text, or the confirmed text while that card is empty. */
export function noteText(note: Note): string {
  return note.draft?.trim() || note.body
}

/** The note with its card closed, or nothing: an element note without words says nothing to act on. */
function settled(note: Note, body: string): Note[] {
  return body || note.kind === 'screenshot' ? [{ ...note, body, draft: undefined }] : []
}

/** Every note with its card closed, keeping what an open one holds. */
function closed(notes: Note[]): Note[] {
  return notes.flatMap((note) => (note.draft === undefined ? [note] : settled(note, noteText(note))))
}

/** Closes whichever card is open, keeping what it holds. */
export function closeNotes(runID: string): void {
  patchFeedback(runID, { notes: closed(feedback(runID).notes) })
}

/** Adds a note with its card open, which closes any other. */
export function addNote(runID: string, note: Note): void {
  patchFeedback(runID, { notes: [...closed(feedback(runID).notes), { ...note, draft: '' }], sent: undefined })
}

export function updateNote(runID: string, id: string, patch: Partial<ElementNote> | Partial<ScreenshotNote>): void {
  patchFeedback(runID, { notes: feedback(runID).notes.map((note) => (note.id === id ? { ...note, ...patch } as Note : note)) })
}

export function removeNote(runID: string, id: string): void {
  patchFeedback(runID, { notes: feedback(runID).notes.filter((note) => note.id !== id) })
}

/** Reopens a confirmed note's card on its text, which closes any other. */
export function editNote(runID: string, id: string): void {
  patchFeedback(runID, { notes: closed(feedback(runID).notes).map((note) => (note.id === id ? { ...note, draft: note.body } : note)) })
}

/** Closes a note's card on what it holds, or on what it held before it opened. */
export function closeNote(runID: string, id: string, keep: boolean): void {
  patchFeedback(runID, {
    notes: feedback(runID).notes.flatMap((note) => (note.id === id ? settled(note, keep ? (note.draft ?? note.body).trim() : note.body) : [note])),
  })
}

export function logKey(entry: Pick<LogEntry, 'kind' | 'sequence'>): string {
  return `${entry.kind}:${entry.sequence}`
}

export function selectLogs(runID: string, entries: LogEntry[], selected: boolean): void {
  const keys = new Set(entries.map(logKey))
  const others = feedback(runID).logs.filter((entry) => !keys.has(logKey(entry)))
  patchFeedback(runID, { logs: selected ? [...others, ...entries].sort((a, b) => a.sequence - b.sequence) : others, sent: undefined })
}

/** The notes a send carries: every screenshot, and each element the page
 * described that has words, saying what `noteText` reads. */
export function written(notes: Note[]): Note[] {
  return notes
    .filter((note) => note.kind === 'screenshot' || (note.element !== undefined && noteText(note) !== ''))
    .map((note) => ({ ...note, body: noteText(note), draft: undefined }))
}

function counted(total: number, one: string): string | false {
  return total > 0 && (total === 1 ? `1 ${one}` : `${total} ${one}s`)
}

/** "2 annotations, 1 screenshot, 3 console messages", naming only what is there. */
export function summary(notes: Note[], logs: LogEntry[]): string {
  const elements = notes.filter((note) => note.kind === 'element').length
  const consoles = logs.filter((entry) => entry.kind === 'console').length
  return [
    counted(elements, 'annotation'),
    counted(notes.length - elements, 'screenshot'),
    counted(consoles, 'console message'),
    counted(logs.length - consoles, 'failed request'),
  ].filter(Boolean).join(', ')
}

function clip(value: string, max: number): string {
  return value.length > max ? `${value.slice(0, max)}…` : value
}

/** `button "Save changes"`: the element as a person would name it. */
export function elementLabel(element: DevBrowserElement): string {
  const named = element.name || element.text
  return named ? `${element.tag} "${clip(named, 60)}"` : element.tag
}

function pageLine(page: NotePage): string {
  return `${page.url} (viewport ${page.width}x${page.height})`
}

/** The time of day in UTC, marked as such: the agent does not know the reader's zone. */
function clock(time: string): string {
  const at = new Date(time)
  return Number.isNaN(at.valueOf()) ? time : `${at.toISOString().slice(11, 19)}Z`
}

/** What a row says after its time: the level and text, or the request and how it failed. */
function logText(entry: LogEntry): string {
  if (entry.kind === 'console') return `${entry.level} ${entry.text}`
  return `${entry.method} ${entry.url} ${entry.status || `failed: ${entry.failure || 'no response'}`}`
}

function elementBlock(note: ElementNote, element: DevBrowserElement, index: number, shared: boolean): string {
  const { bounds } = element
  const attributes = Object.entries(element.attributes ?? {}).map(([key, value]) => `${key}="${value}"`).join(' ')
  const styles = Object.entries(element.styles ?? {}).map(([key, value]) => `${key}: ${value}`).join('; ')
  const facts = [
    !shared && `Page: ${pageLine(note.page)}`,
    note.path && `Screenshot: ${note.path}`,
    `Selector: ${element.selector}`,
    element.frame_url && `Frame: ${element.frame_url}`,
    element.source && `Source: ${element.source}`,
    element.component && `Component: ${element.component}`,
    element.role && `Role: ${element.role}`,
    element.name && `Name: ${clip(element.name, 200)}`,
    element.text && `Text: ${clip(element.text, 300)}`,
    attributes && `Attributes: ${clip(attributes, 600)}`,
    `Bounds: x ${Math.round(bounds.x)}, y ${Math.round(bounds.y)}, ${Math.round(bounds.width)} x ${Math.round(bounds.height)}`,
    styles && `Styles: ${styles}`,
  ].filter(Boolean).map((line) => `- ${line}`)
  return [
    `${index}. ${elementLabel(element)}`,
    note.body,
    ...facts,
    ...(element.html ? [fenced('html', [clip(element.html, 1200)])] : []),
  ].join('\n')
}

function screenshotBlock(note: ScreenshotNote, index: number, shared: boolean): string {
  return [
    `${index}. Screenshot of the page`,
    ...(note.body ? [note.body] : []),
    ...(shared ? [] : [`- Page: ${pageLine(note.page)}`]),
    `- Screenshot: ${note.artifact.path}`,
  ].join('\n')
}

function logBlock(title: string, entries: LogEntry[]): string | false {
  const pages = [...new Set(entries.map((entry) => entry.page))]
  return entries.length > 0 && [`${title} (${pages.join(', ')}):`, ...entries.map((entry) => `- ${clock(entry.time)} ${clip(logText(entry), 500)}`)].join('\n')
}

/**
 * The one message a batch becomes. Each note says what the person wrote
 * first and then what the page says about the element, so the agent can find
 * it in the source without the Browser. `notes` are what `written` returns.
 */
export function feedbackMessage(notes: Note[], logs: LogEntry[]): string {
  const pages = [...new Set(notes.map((note) => pageLine(note.page)))]
  const shared = pages.length === 1
  const pictured = notes.some((note) => note.kind === 'screenshot' || note.path)
  return [
    [
      `Feedback from the page in this run's browser: ${summary(notes, logs)}.`,
      pictured && 'Each screenshot path is an image file inside the run; open it to see what I saw.',
      shared && `Page: ${pages[0]}`,
    ].filter(Boolean).join('\n'),
    ...notes.map((note, at) => (note.kind === 'screenshot' ? screenshotBlock(note, at + 1, shared) : elementBlock(note, note.element!, at + 1, shared))),
    logBlock('Console', logs.filter((entry) => entry.kind === 'console')),
    logBlock('Failed requests', logs.filter((entry) => entry.kind === 'network')),
  ].filter(Boolean).join('\n\n')
}
