import type { AssistantConfirmation, AssistantStatus, ViewContext } from '@/lib/assistant-types'
import { mobileScreen } from '@/lib/breakpoints'
import { randomID } from '@/lib/utils'
import type { RootState } from '@/store'
import type { SliceCreator } from '@/store/slice'
import { draftConflict, entityKey, maxAttachments, pinPage, sameAttachment, type AttachedTarget, type DraftPin } from '@/store/view-context'

/** The assistant's transcript lives in `acpSessions` under this key, never in `runs`. */
export const assistantSessionID = 'assistant'

export const minAssistantWidth = 320
export const maxAssistantWidth = 640
export const defaultAssistantWidth = 400

export function onPhone(): boolean {
  return typeof window !== 'undefined' && window.matchMedia(mobileScreen).matches
}

export function assistantWaiting(s: Pick<RootState, 'assistantConfirmations' | 'acpSessions'>): number {
  return s.assistantConfirmations.length + (s.acpSessions[assistantSessionID]?.pending.length ?? 0)
}

export function clampAssistantWidth(width: number): number {
  return Math.min(maxAssistantWidth, Math.max(minAssistantWidth, width))
}

export interface AssistantAttempt {
  text: string
  key: string
  viewContext?: ViewContext
  /** The chips the prompt carries, so a send that lands clears only them. */
  attachments: AttachedTarget[]
}

/** What an entry point such as **Fix this** puts in the panel. */
export interface AssistantAsk {
  draft: string
  attachments?: AttachedTarget[]
  /** Ask about the page the member is on, over the one a draft was started on. */
  page?: boolean
}

/** A second tab sending the same draft while the first tab's send is in flight, for at most this long, reuses its idempotency key, so the server runs it once. */
export const duplicateSendWindow = 10_000
const sentKey = 'aether.assistant.sent'

let tab: string | undefined

interface SentPrompt { tab?: string; hash?: string; key?: string; at?: number }

function heldPrompt(): SentPrompt | null {
  try {
    return JSON.parse(localStorage.getItem(sentKey) ?? 'null') as SentPrompt | null
  } catch {
    return null
  }
}

/**
 * The idempotency key for a new prompt: another tab's, while it is still
 * sending the same text and context. Only a hash of the draft is stored.
 * Without crypto.subtle there is no hash, and no reuse.
 */
export async function promptKey(fingerprint: string, now = Date.now()): Promise<string> {
  if (typeof crypto === 'undefined' || !crypto.subtle) return randomID('assistant')
  tab ??= randomID('assistant')
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(fingerprint))
  const hash = Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('')
  const held = heldPrompt()
  const key = held?.key && held.tab !== tab && held.hash === hash && now - (held.at ?? 0) < duplicateSendWindow ? held.key : randomID('assistant')
  try {
    localStorage.setItem(sentKey, JSON.stringify({ tab, hash, key, at: now }))
  } catch {
    // Storage may be full or off; the key still guards this tab's retries.
  }
  return key
}

/** The send under `key` settled: a later prompt with the same text is a new message, such as a second "yes". */
export function settlePromptKey(key: string) {
  try {
    if (heldPrompt()?.key === key) localStorage.removeItem(sentKey)
  } catch {
    // Nothing was stored.
  }
}

export interface AssistantSlice {
  assistantOpen: boolean
  assistantWidth: number
  assistantStatus: AssistantStatus | null
  assistantStatusError: string | null
  assistantConfirmations: AssistantConfirmation[]
  assistantDraft: string
  /** The prompt being sent, its idempotency key and view context. Kept with the draft so reopening the panel retries under the same key and context. */
  assistantAttempt: AssistantAttempt | null
  assistantSending: boolean
  /** assistant.cancel is in flight. */
  assistantStopping: boolean
  /** The last send or stop failure, or attachments dropped past the cap, until the draft changes or the next send. */
  assistantError: string | null
  /** What the member pointed the draft at. Navigation never changes them. */
  assistantAttachments: AttachedTarget[]
  /** The page the draft was started on; null while there is nothing to send. */
  assistantPin: DraftPin | null
  /** The member removed the page from the next message. */
  assistantPageRemoved: boolean
  /** An entry point filled the panel: the composer takes focus once and clears this. */
  assistantFocus: boolean
  /** Bumped by a new conversation so the stream reconnects from sequence 0. */
  assistantGeneration: number
  /** Run notices from the assistant that arrived while the panel was closed. */
  assistantNotices: number
  noteAssistantNotice: () => void
  /** Bumped when a card's Undo changes a preference, so Settings › Memory reads the list again. */
  preferenceUndos: number
  notePreferenceUndo: () => void
  setAssistantOpen: (open: boolean) => void
  setAssistantWidth: (width: number) => void
  setAssistantStatus: (status: AssistantStatus) => void
  setAssistantStatusError: (error: string | null) => void
  setAssistantDraft: (draft: string) => void
  setAssistantAttempt: (attempt: AssistantAttempt | null) => void
  setAssistantSending: (sending: boolean) => void
  setAssistantStopping: (stopping: boolean) => void
  setAssistantError: (error: string | null) => void
  takeAssistantFocus: () => void
  /** Opens the panel with the attachments and, unless the member has a draft, the draft. Never sends. */
  askAssistant: (ask: AssistantAsk) => void
  removeAssistantAttachment: (index: number) => void
  setAssistantPageRemoved: (removed: boolean) => void
  /** Answers the conflict after navigating: ask about the current page, or keep the pinned one. */
  chooseAssistantPage: (current: boolean) => void
  /** The prompt was accepted: the next one starts empty. */
  clearAssistantDraft: () => void
  /** `attempt` was accepted: clears its text and chips, keeping chips added while it was sent. */
  settleAssistantSend: (attempt: AssistantAttempt) => void
  restartAssistantConversation: () => void
  /** A different signed-in member must not see the previous one's assistant. */
  resetAssistant: () => void
}

export const createAssistantSlice: SliceCreator<AssistantSlice> = (set, get) => ({
  assistantOpen: false,
  assistantWidth: defaultAssistantWidth,
  assistantStatus: null,
  assistantStatusError: null,
  assistantConfirmations: [],
  assistantDraft: '',
  assistantAttempt: null,
  assistantSending: false,
  assistantStopping: false,
  assistantError: null,
  assistantAttachments: [],
  assistantPin: null,
  assistantPageRemoved: false,
  assistantFocus: false,
  assistantGeneration: 0,
  assistantNotices: 0,
  noteAssistantNotice: () => set((s) => (s.assistantOpen ? s : { assistantNotices: s.assistantNotices + 1 })),
  preferenceUndos: 0,
  notePreferenceUndo: () => set((s) => ({ preferenceUndos: s.preferenceUndos + 1 })),
  setAssistantOpen: (assistantOpen) => set(assistantOpen ? { assistantOpen, assistantNotices: 0 } : { assistantOpen }),
  setAssistantWidth: (width) => set({ assistantWidth: clampAssistantWidth(width) }),
  setAssistantStatus: (assistantStatus) =>
    set({ assistantStatus, assistantStatusError: null, assistantConfirmations: assistantStatus.confirmations ?? [] }),
  setAssistantStatusError: (assistantStatusError) => set({ assistantStatusError }),
  setAssistantDraft: (assistantDraft) =>
    set((s) => {
      const empty = !assistantDraft.trim() && !s.assistantAttachments.length
      return {
        assistantDraft,
        assistantError: null,
        assistantAttempt: s.assistantAttempt?.text === assistantDraft.trim() ? s.assistantAttempt : null,
        assistantPin: empty ? null : s.assistantPin ?? pinPage(s),
        assistantPageRemoved: empty ? false : s.assistantPageRemoved,
      }
    }),
  setAssistantAttempt: (assistantAttempt) => set({ assistantAttempt }),
  setAssistantSending: (assistantSending) => set({ assistantSending }),
  setAssistantStopping: (assistantStopping) => set({ assistantStopping }),
  setAssistantError: (assistantError) => set({ assistantError }),
  takeAssistantFocus: () => set({ assistantFocus: false }),
  askAssistant: ({ draft, attachments = [], page = false }) =>
    set((s) => {
      const kept = s.assistantAttachments.filter((held) => !attachments.some((added) => sameAttachment(held, added)))
      const keepDraft = s.assistantDraft.trim() !== ''
      const all = [...kept, ...attachments]
      const dropped = all.slice(0, -maxAttachments).map((target) => `${target.kind === 'run' ? 'session' : target.kind} ${target.label}`)
      return {
        assistantOpen: true,
        assistantNotices: 0,
        assistantDraft: keepDraft ? s.assistantDraft : draft,
        assistantAttachments: all.slice(-maxAttachments),
        assistantPin: page ? pinPage(s) : s.assistantPin ?? pinPage(s),
        assistantPageRemoved: page ? false : s.assistantPageRemoved,
        assistantAttempt: null,
        assistantError: dropped.length ? `A message carries at most ${maxAttachments} attachments; removed ${dropped.join(', ')}.` : null,
        assistantFocus: true,
      }
    }),
  removeAssistantAttachment: (index) =>
    set((s) => ({ assistantAttachments: s.assistantAttachments.filter((_, i) => i !== index), assistantAttempt: null })),
  setAssistantPageRemoved: (assistantPageRemoved) => set({ assistantPageRemoved, assistantAttempt: null }),
  chooseAssistantPage: (current) =>
    set((s) => {
      if (!s.assistantPin) return s
      if (current) return { assistantPin: pinPage(s), assistantAttempt: null }
      const moved = draftConflict(s)
      return moved ? { assistantPin: { ...s.assistantPin, keptOver: entityKey(moved) } } : s
    }),
  clearAssistantDraft: () =>
    set({ assistantDraft: '', assistantAttempt: null, assistantAttachments: [], assistantPin: null, assistantPageRemoved: false, assistantError: null }),
  settleAssistantSend: (attempt) => {
    const s = get()
    const draft = s.assistantDraft.trim() === attempt.text ? '' : s.assistantDraft
    const kept = s.assistantAttachments.filter((held) => !attempt.attachments.some((sent) => sameAttachment(held, sent)))
    if (!draft.trim() && !kept.length) return get().clearAssistantDraft()
    set({ assistantDraft: draft, assistantAttachments: kept, assistantAttempt: null, assistantPin: pinPage(s), assistantPageRemoved: false, assistantError: null })
  },
  restartAssistantConversation: () =>
    set((s) => {
      const { [assistantSessionID]: _dropped, ...rest } = s.acpSessions
      return { acpSessions: rest, assistantConfirmations: [], assistantAttempt: null, assistantSending: false, assistantStopping: false, assistantError: null, assistantGeneration: s.assistantGeneration + 1 }
    }),
  resetAssistant: () => {
    get().restartAssistantConversation()
    get().clearAssistantDraft()
    set({ assistantStatus: null, assistantStatusError: null, assistantStopping: false, assistantNotices: 0, assistantShareChoice: null })
  },
})
