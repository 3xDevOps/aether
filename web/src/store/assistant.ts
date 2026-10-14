import type { AssistantConfirmation, AssistantStatus } from '@/lib/assistant-types'
import { mobileScreen } from '@/lib/breakpoints'
import type { SliceCreator } from '@/store/slice'

/** The assistant's transcript lives in `acpSessions` under this key, never in `runs`. */
export const assistantSessionID = 'assistant'

export const minAssistantWidth = 320
export const maxAssistantWidth = 640
export const defaultAssistantWidth = 400

export function onPhone(): boolean {
  return typeof window !== 'undefined' && window.matchMedia(mobileScreen).matches
}

export function clampAssistantWidth(width: number): number {
  return Math.min(maxAssistantWidth, Math.max(minAssistantWidth, width))
}

export interface AssistantSlice {
  assistantOpen: boolean
  assistantWidth: number
  assistantStatus: AssistantStatus | null
  assistantStatusError: string | null
  /** The pending set of the last status; replaced whole, never patched. */
  assistantConfirmations: AssistantConfirmation[]
  assistantDraft: string
  /** Bumped by a new conversation so the stream reconnects from sequence 0. */
  assistantGeneration: number
  setAssistantOpen: (open: boolean) => void
  setAssistantWidth: (width: number) => void
  setAssistantStatus: (status: AssistantStatus) => void
  setAssistantStatusError: (error: string | null) => void
  setAssistantDraft: (draft: string) => void
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
  assistantGeneration: 0,
  setAssistantOpen: (assistantOpen) => set({ assistantOpen }),
  setAssistantWidth: (width) => set({ assistantWidth: clampAssistantWidth(width) }),
  setAssistantStatus: (assistantStatus) =>
    set({ assistantStatus, assistantStatusError: null, assistantConfirmations: assistantStatus.confirmations ?? [] }),
  setAssistantStatusError: (assistantStatusError) => set({ assistantStatusError }),
  setAssistantDraft: (assistantDraft) => set({ assistantDraft }),
  restartAssistantConversation: () =>
    set((s) => {
      const { [assistantSessionID]: _dropped, ...rest } = s.acpSessions
      return { acpSessions: rest, assistantConfirmations: [], assistantGeneration: s.assistantGeneration + 1 }
    }),
  resetAssistant: () => {
    get().restartAssistantConversation()
    set({ assistantStatus: null, assistantStatusError: null, assistantDraft: '' })
  },
})
