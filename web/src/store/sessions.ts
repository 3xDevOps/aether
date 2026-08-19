import type { Session } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface SessionsSlice {
  sessions: Record<string, Session>
  setSessions: (sessions: Session[]) => void
  upsertSession: (session: Session) => void
}

export const createSessionsSlice: SliceCreator<SessionsSlice> = (set) => ({
  sessions: {},
  setSessions: (sessions) =>
    set({ sessions: Object.fromEntries(sessions.map((s) => [s.id, s])) }),
  upsertSession: (session) =>
    set((s) => ({ sessions: { ...s.sessions, [session.id]: session } })),
})
