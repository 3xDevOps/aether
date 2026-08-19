import type { ConnectionState } from '@/lib/stream'
import type { SliceCreator } from '@/store/slice'

/**
 * What the terminal view knows about one run's attach. `write` is what the
 * user asked for; `steerDenied` is the server's answer, which is the only
 * thing that gates steering - the client never decides it.
 */
export interface TerminalState {
  connection: ConnectionState
  write: boolean
  steerDenied: boolean
  /** The server's last refusal, shown above the terminal. */
  message: string | null
  /** The attach was refused outright, so no reconnect is pending. */
  refused: boolean
}

export const initialTerminal: TerminalState = {
  connection: 'connecting',
  write: false,
  steerDenied: false,
  message: null,
  refused: false,
}

export interface TerminalSlice {
  terminals: Record<string, TerminalState>
  setTerminal: (runID: string, patch: Partial<TerminalState>) => void
}

export const createTerminalSlice: SliceCreator<TerminalSlice> = (set) => ({
  terminals: {},
  setTerminal: (runID, patch) =>
    set((s) => ({
      terminals: {
        ...s.terminals,
        [runID]: { ...(s.terminals[runID] ?? initialTerminal), ...patch },
      },
    })),
})
