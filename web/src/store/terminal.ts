import type { ConnectionState } from '@/lib/stream'
import type { Attachment } from '@/routes/terminal/attach'
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

export interface RunShellDockState {
  tabs: string[]
  activeTab: string | null
  collapsed: boolean
  refusedMessage: string | null
}

export const initialRunShellDock: RunShellDockState = {
  tabs: [],
  activeTab: null,
  collapsed: false,
  refusedMessage: null,
}

/** The socket handle is deliberately kept outside Zustand's persisted state. */
export type RunShellSocket = Attachment

const shellSockets = new Map<string, RunShellSocket>()
const shellSocketKey = (runID: string, tab: string) => `${runID}:${tab}`

export function registerShellSocket(
  runID: string,
  tab: string,
  socket: RunShellSocket,
): void {
  const key = shellSocketKey(runID, tab)
  shellSockets.get(key)?.close()
  shellSockets.set(key, socket)
}

export function getShellSocket(runID: string, tab: string): RunShellSocket | undefined {
  return shellSockets.get(shellSocketKey(runID, tab))
}

export function subscribeShellSocket(
  runID: string,
  tab: string,
  onData: (chunk: Uint8Array) => void,
): () => void {
  // A socket remains owned by this module while the route changes. Output is
  // delivered only to the currently mounted terminal host.
  const key = shellSocketKey(runID, tab)
  const listeners = shellSocketListeners.get(key) ?? new Set<(chunk: Uint8Array) => void>()
  listeners.add(onData)
  shellSocketListeners.set(key, listeners)
  return () => {
    listeners.delete(onData)
    if (listeners.size === 0) shellSocketListeners.delete(key)
  }
}

function emitShellSocketData(runID: string, tab: string, chunk: Uint8Array): void {
  shellSocketListeners.get(shellSocketKey(runID, tab))?.forEach((listener) => listener(chunk))
}

const shellSocketListeners = new Map<string, Set<(chunk: Uint8Array) => void>>()

export function unregisterShellSocket(runID: string, tab: string): void {
  const key = shellSocketKey(runID, tab)
  shellSockets.get(key)?.close()
  shellSockets.delete(key)
  shellSocketListeners.delete(key)
}

/** Used by the dock to build handlers without putting callbacks in Zustand. */
export { emitShellSocketData }

export interface TerminalSlice {
  terminals: Record<string, TerminalState>
  shellDocks: Record<string, RunShellDockState>
  setTerminal: (runID: string, patch: Partial<TerminalState>) => void
  openShellTab: (runID: string) => string | null
  closeShellTab: (runID: string, tab: string) => void
  selectShellTab: (runID: string, tab: string) => void
  setDockCollapsed: (runID: string, collapsed: boolean) => void
  setShellRefused: (runID: string, message: string | null) => void
  removeShellTab: (runID: string, tab: string) => void
}

const dock = (docks: Record<string, RunShellDockState>, runID: string) =>
  docks[runID] ?? initialRunShellDock

export const createTerminalSlice: SliceCreator<TerminalSlice> = (set) => ({
  terminals: {},
  shellDocks: {},
  setTerminal: (runID, patch) =>
    set((s) => ({
      terminals: {
        ...s.terminals,
        [runID]: { ...(s.terminals[runID] ?? initialTerminal), ...patch },
      },
    })),
  openShellTab: (runID) => {
    let opened: string | null = null
    set((s) => {
      const current = dock(s.shellDocks, runID)
      if (current.tabs.length >= 4) return s
      for (let n = 1; n <= 4; n++) {
        const tab = `t${n}`
        if (!current.tabs.includes(tab)) {
          opened = tab
          break
        }
      }
      if (!opened) return s
      return {
        shellDocks: {
          ...s.shellDocks,
          [runID]: {
            ...current,
            tabs: [...current.tabs, opened],
            activeTab: opened,
            refusedMessage: null,
          },
        },
      }
    })
    return opened
  },
  closeShellTab: (runID, tab) => {
    unregisterShellSocket(runID, tab)
    set((s) => {
      const current = s.shellDocks[runID]
      if (!current || !current.tabs.includes(tab)) return s
      const tabs = current.tabs.filter((entry) => entry !== tab)
      return {
        shellDocks: {
          ...s.shellDocks,
          [runID]: {
            ...current,
            tabs,
            activeTab:
              current.activeTab === tab ? (tabs[tabs.length - 1] ?? null) : current.activeTab,
            refusedMessage: tabs.length === 0 ? null : current.refusedMessage,
          },
        },
      }
    })
  },
  selectShellTab: (runID, tab) =>
    set((s) => {
      const current = s.shellDocks[runID]
      if (!current?.tabs.includes(tab)) return s
      return { shellDocks: { ...s.shellDocks, [runID]: { ...current, activeTab: tab } } }
    }),
  setDockCollapsed: (runID, collapsed) =>
    set((s) => ({
      shellDocks: {
        ...s.shellDocks,
        [runID]: { ...dock(s.shellDocks, runID), collapsed },
      },
    })),
  setShellRefused: (runID, message) =>
    set((s) => ({
      shellDocks: {
        ...s.shellDocks,
        [runID]: { ...dock(s.shellDocks, runID), refusedMessage: message },
      },
    })),
  removeShellTab: (runID, tab) => {
    // Shell exit and a user close have identical state semantics.
    unregisterShellSocket(runID, tab)
    set((s) => {
      const current = s.shellDocks[runID]
      if (!current?.tabs.includes(tab)) return s
      const tabs = current.tabs.filter((entry) => entry !== tab)
      return {
        shellDocks: {
          ...s.shellDocks,
          [runID]: {
            ...current,
            tabs,
            activeTab:
              current.activeTab === tab ? (tabs[tabs.length - 1] ?? null) : current.activeTab,
          },
        },
      }
    })
  },
})
