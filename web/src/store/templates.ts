import { toast } from 'sonner'
import { create } from 'zustand'
import type { Api } from '@/lib/api'
import type { Schedule, Template } from '@/lib/types'
import { sameIdentity, useStore } from '@/store'

export const saveKey = (kind: 'template' | 'schedule', workspaceID: string, name: string) => JSON.stringify([kind, workspaceID, name])

export type Lists = { templates: Template[] | null; schedules: Schedule[] }
type Changes = { seq: number; templates: Map<string, number>; schedules: Map<string, number>; reads: number; applied: Record<keyof Lists, number> }

// Outlive the Templates route, so an action sent before leaving it still holds Delete and Unschedule, and lands on the list, after coming back.
export const useSaves = create<Record<string, number[]>>(() => ({}))
export const useLists = create<Record<string, Lists>>(() => ({}))
const latestSave = new Map<string, number>()
// When each template and schedule last changed here, per workspace, so a list read sent before keeps that change.
const changes = new Map<string, Changes>()
// Each pending save's prediction, mapped to the server value it replaced.
export const savingTemplates = new Map<Template, Template | undefined>()
export const savingSchedules = new Map<Schedule, Schedule | undefined>()
// Keyed by what an action sends, not by mutation: a dialog's Retry is a new mutation, so optimistic() cannot drop the late refusal it supersedes.
const confirms = new Map<string, number>()
// Per action, the toast showing its failure, so a late success dismisses it.
export const failureToasts = new Map<string, string | number>()
let saves = 0

useStore.subscribe((state, previous) => {
  if (state.identityKey === previous.identityKey) return
  useSaves.setState({}, true)
  useLists.setState({}, true)
  for (const held of [latestSave, changes, savingTemplates, savingSchedules, confirms, failureToasts]) held.clear()
})

const confirmed = (action: string) => confirms.get(action) ?? 0
export const answered = (action: string) => {
  confirms.set(action, confirmed(action) + 1)
  const shown = failureToasts.get(action)
  if (shown === undefined) return
  toast.dismiss(shown)
  failureToasts.delete(action)
}
export type Started = { action: string; superseded: () => boolean; same: () => boolean }
export const started = (action: string): Started => {
  const sent = confirmed(action)
  return { action, superseded: () => confirmed(action) > sent, same: sameIdentity() }
}

export function changesIn(workspaceID: string): Changes {
  let since = changes.get(workspaceID)
  if (!since) changes.set(workspaceID, (since = { seq: 0, templates: new Map(), schedules: new Map(), reads: 0, applied: { templates: 0, schedules: 0 } }))
  return since
}

export function mark(workspaceID: string, kind: 'templates' | 'schedules', name: string) {
  const since = changesIn(workspaceID)
  since[kind].set(name, ++since.seq)
}

export const unread: Lists = { templates: null, schedules: [] }

export function setTemplates(workspaceID: string, next: (list: Template[] | null) => Template[] | null) {
  useLists.setState((all) => {
    const lists = all[workspaceID] ?? unread
    return { [workspaceID]: { ...lists, templates: next(lists.templates) } }
  })
}

export function setSchedules(workspaceID: string, next: (list: Schedule[]) => Schedule[]) {
  useLists.setState((all) => {
    const lists = all[workspaceID] ?? unread
    return { [workspaceID]: { ...lists, schedules: next(lists.schedules) } }
  })
}

export function sendSave<T>(key: string, send: () => Promise<T>): { sent: Promise<T>; latest: () => boolean } {
  const order = ++saves
  latestSave.set(key, order)
  useSaves.setState((sending) => ({ [key]: [...(sending[key] ?? []), order] }))
  const sent = send().finally(() => useSaves.setState((sending) => ({ [key]: (sending[key] ?? []).filter((o) => o !== order) })))
  return { sent, latest: () => latestSave.get(key) === order }
}

export const byName = (a: Template, b: Template) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0)

export function withTemplate(list: Template[] | null, name: string, next?: Template): Template[] | null {
  if (!list) return next ? [next] : list
  const rest = list.filter((t) => t.name !== name)
  return next ? [...rest, next].sort(byName) : rest
}

export function withSchedule(list: Schedule[], template: string, next?: Schedule): Schedule[] {
  const rest = list.filter((s) => s.template !== template)
  return next ? [...rest, next] : rest
}

/**
 * Returns a merge for a list read sent now: entries changed here after it was sent, or with a save still unanswered, keep their shown value, since the read may predate the change.
 * It returns null, to keep the shown list, for an answer to a read sent before the last one applied or before the signed-in identity changed.
 */
export function sinceRead<T>(workspaceID: string, kind: 'templates' | 'schedules', name: (item: T) => string) {
  const since = changesIn(workspaceID)
  const sent = since.seq
  const order = ++since.reads
  const same = sameIdentity()
  const saving = (item: T) => Boolean(useSaves.getState()[saveKey(kind === 'templates' ? 'template' : 'schedule', workspaceID, name(item))]?.length)
  const changed = (item: T) => (since[kind].get(name(item)) ?? 0) > sent || saving(item)
  return (read: T[], shown: T[]): T[] | null => {
    if (!same() || order < since.applied[kind]) return null
    since.applied[kind] = order
    return [...read.filter((i) => !changed(i)), ...shown.filter(changed)]
  }
}

/** Reads a workspace's templates into the shared list through sinceRead; rejects with the server's error. */
export async function readTemplates(client: Pick<Api, 'templateList'>, workspaceID: string): Promise<void> {
  const templates = sinceRead<Template>(workspaceID, 'templates', (t) => t.name)
  const list = await client.templateList(workspaceID)
  setTemplates(workspaceID, (current) => templates(list, current ?? [])?.sort(byName) ?? current)
}

/** Reads a workspace's schedules into the shared list through sinceRead; rejects with the server's error. */
export async function readSchedules(client: Pick<Api, 'scheduleList'>, workspaceID: string): Promise<void> {
  const schedules = sinceRead<Schedule>(workspaceID, 'schedules', (s) => s.template)
  const read = await client.scheduleList(workspaceID)
  setSchedules(workspaceID, (current) => schedules(read, current) ?? current)
}
