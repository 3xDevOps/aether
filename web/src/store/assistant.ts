import type { AssistantConfirmation, AssistantStatus, ViewContext } from '@/lib/assistant-types'
import { mobileScreen } from '@/lib/breakpoints'
import type { RootState } from '@/store'
import type { SliceCreator } from '@/store/slice'

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
  /** Bumped by a new conversation so the stream reconnects from sequence 0. */
  assistantGeneration: number
  /** Run notices from the assistant that arrived while the panel was closed. */
  assistantNotices: number
  noteAssistantNotice: () => void
  setAssistantOpen: (open: boolean) => void
  setAssistantWidth: (width: number) => void
  setAssistantStatus: (status: AssistantStatus) => void
  setAssistantStatusError: (error: string | null) => void
  setAssistantDraft: (draft: string) => void
  setAssistantAttempt: (attempt: AssistantAttempt | null) => void
  setAssistantSending: (sending: boolean) => void
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
  assistantGeneration: 0,
  assistantNotices: 0,
  noteAssistantNotice: () => set((s) => (s.assistantOpen ? s : { assistantNotices: s.assistantNotices + 1 })),
  setAssistantOpen: (assistantOpen) => set(assistantOpen ? { assistantOpen, assistantNotices: 0 } : { assistantOpen }),
  setAssistantWidth: (width) => set({ assistantWidth: clampAssistantWidth(width) }),
  setAssistantStatus: (assistantStatus) =>
    set({ assistantStatus, assistantStatusError: null, assistantConfirmations: assistantStatus.confirmations ?? [] }),
  setAssistantStatusError: (assistantStatusError) => set({ assistantStatusError }),
  setAssistantDraft: (assistantDraft) =>
    set((s) => ({
      assistantDraft,
      assistantAttempt: s.assistantAttempt?.text === assistantDraft.trim() ? s.assistantAttempt : null,
    })),
  setAssistantAttempt: (assistantAttempt) => set({ assistantAttempt }),
  setAssistantSending: (assistantSending) => set({ assistantSending }),
  restartAssistantConversation: () =>
    set((s) => {
      const { [assistantSessionID]: _dropped, ...rest } = s.acpSessions
      return { acpSessions: rest, assistantConfirmations: [], assistantAttempt: null, assistantSending: false, assistantGeneration: s.assistantGeneration + 1 }
    }),
  resetAssistant: () => {
    get().restartAssistantConversation()
    set({ assistantStatus: null, assistantStatusError: null, assistantDraft: '', assistantNotices: 0, assistantShareChoice: null })
  },
})
