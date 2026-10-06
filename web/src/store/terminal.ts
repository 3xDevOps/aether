import type { ConnectionState } from '@/lib/stream'
import type { DevTerminal } from '@/lib/types'
import type { AttachDataKind, AttachDataResult, Attachment } from '@/routes/terminal/attach'
import type { SliceCreator } from '@/store/slice'

/**
 * What the terminal view knows about one run's attach. `write` is the
 * server-granted control state; it never becomes true until an attach ack or
 * an acknowledged control request grants the lease.
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

/** Proves remembered attach state still belongs to the same authenticated run. */
export interface TerminalRunFence {
  identityKey: string
  terminalCacheEpoch: number
  authorityKey: string
  runCreatedAt: string
}

/**
 * A user's explicit preference for the next attach. This is deliberately
 * limited to one boolean plus the fences; transport, replay, and xterm state
 * stay owned by the mounted route.
 */
export interface TerminalWriteIntent extends TerminalRunFence {
  write: boolean
}

/**
 * The tab's control-session identity for a run, so a remount can reclaim the
 * disconnected lease its previous mount held. Never persisted: a reload is a
 * new session.
 */
export interface TerminalControlSession extends TerminalRunFence {
  controlSessionID: string
}

export interface RunShellDockState {
  terminals: DevTerminal[]
  /** Hidden incarnations stay discoverable without reappearing on each poll. */
  hidden: string[]
  tabs: string[]
  activeTab: string | null
  shellShown: boolean
  refusedMessage: string | null
}

export const initialRunShellDock: RunShellDockState = {
  terminals: [],
  hidden: [],
  tabs: [],
  activeTab: null,
  shellShown: false,
  refusedMessage: null,
}

/** The socket handle is deliberately kept outside Zustand's persisted state. */
export type RunShellSocket = Attachment
type ShellSocketDataListener = (
  chunk: Uint8Array,
  kind: AttachDataKind,
  settled?: () => void,
) => AttachDataResult

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
  onData: ShellSocketDataListener,
): () => void {
  // A socket remains owned by this module while the route changes. Output is
  // delivered only to the currently mounted terminal host.
  const key = shellSocketKey(runID, tab)
  const listeners = shellSocketListeners.get(key) ?? new Set<ShellSocketDataListener>()
  listeners.add(onData)
  shellSocketListeners.set(key, listeners)
  return () => {
    listeners.delete(onData)
    if (listeners.size === 0) shellSocketListeners.delete(key)
  }
}

function emitShellSocketData(
  runID: string,
  tab: string,
  chunk: Uint8Array,
  kind: AttachDataKind,
  settled?: () => void,
): AttachDataResult {
  const listeners = shellSocketListeners.get(shellSocketKey(runID, tab))
  if (!listeners) {
    settled?.()
    return
  }

  let firstCompletion: Promise<void> | undefined
  let completions: Promise<void>[] | undefined
  listeners.forEach((listener) => {
    const completion = listener(chunk, kind, settled)
    if (!completion || typeof completion.then !== 'function') return
    const previous = firstCompletion
    if (!previous) {
      firstCompletion = completion
    } else if (!completions) {
      completions = [previous, completion]
    } else {
      completions.push(completion)
    }
  })
  if (completions) return Promise.all(completions).then(() => undefined)
  return firstCompletion
}

const shellSocketListeners = new Map<string, Set<ShellSocketDataListener>>()

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
  terminalWriteIntents: Record<string, TerminalWriteIntent>
  terminalControlSessions: Record<string, TerminalControlSession>
  shellDocks: Record<string, RunShellDockState>
  setTerminal: (runID: string, patch: Partial<TerminalState>) => void
  setTerminalWriteIntent: (runID: string, intent: TerminalWriteIntent) => void
  clearTerminalWriteIntent: (runID: string) => void
  setTerminalControlSession: (runID: string, session: TerminalControlSession) => void
  clearTerminalControlSession: (runID: string) => void
  syncShellTerminals: (runID: string, terminals: DevTerminal[]) => void
  closeShellTab: (runID: string, tab: string) => void
  selectShellTab: (runID: string, tab: string) => void
  setShellShown: (runID: string, shown: boolean) => void
  setShellRefused: (runID: string, message: string | null) => void
}

const dock = (docks: Record<string, RunShellDockState>, runID: string) =>
  docks[runID] ?? initialRunShellDock

export const createTerminalSlice: SliceCreator<TerminalSlice> = (set) => ({
  terminals: {},
  terminalWriteIntents: {},
  terminalControlSessions: {},
  shellDocks: {},
  setTerminal: (runID, patch) =>
    set((s) => ({
      terminals: {
        ...s.terminals,
        [runID]: { ...(s.terminals[runID] ?? initialTerminal), ...patch },
      },
    })),
  setTerminalWriteIntent: (runID, intent) =>
    set((s) => ({
      terminalWriteIntents: { ...s.terminalWriteIntents, [runID]: intent },
    })),
  clearTerminalWriteIntent: (runID) =>
    set((s) => {
      if (!s.terminalWriteIntents[runID]) return s
      const terminalWriteIntents = { ...s.terminalWriteIntents }
      delete terminalWriteIntents[runID]
      return { terminalWriteIntents }
    }),
  setTerminalControlSession: (runID, session) =>
    set((s) => ({
      terminalControlSessions: { ...s.terminalControlSessions, [runID]: session },
    })),
  clearTerminalControlSession: (runID) =>
    set((s) => {
      if (!s.terminalControlSessions[runID]) return s
      const terminalControlSessions = { ...s.terminalControlSessions }
      delete terminalControlSessions[runID]
      return { terminalControlSessions }
    }),
  syncShellTerminals: (runID, terminals) => {
    set((s) => {
      const current = dock(s.shellDocks, runID)
      for (const previous of current.terminals) {
        const next = terminals.find((item) => item.terminal_id === previous.terminal_id)
        if (!next || next.incarnation !== previous.incarnation || next.process.state !== 'running') {
          unregisterShellSocket(runID, previous.terminal_id)
        }
      }
      const hidden = current.hidden.filter((key) =>
        terminals.some((item) => `${item.terminal_id}:${item.incarnation}` === key))
      const tabs = terminals.filter((item) =>
        !hidden.includes(`${item.terminal_id}:${item.incarnation}`)).map((item) => item.terminal_id)
      return {
        shellDocks: {
          ...s.shellDocks,
          [runID]: {
            ...current, terminals, hidden, tabs,
            activeTab: current.activeTab && tabs.includes(current.activeTab)
              ? current.activeTab : tabs[0] ?? null,
            shellShown: current.shellShown && tabs.length > 0,
          },
        },
      }
    })
  },
  closeShellTab: (runID, tab) => {
    unregisterShellSocket(runID, tab)
    set((s) => {
      const current = s.shellDocks[runID]
      if (!current || !current.tabs.includes(tab)) return s
      const tabs = current.tabs.filter((entry) => entry !== tab)
      const terminal = current.terminals.find((item) => item.terminal_id === tab)
      return {
        shellDocks: {
          ...s.shellDocks,
          [runID]: {
            ...current,
            tabs,
            hidden: terminal
              ? [...current.hidden, `${terminal.terminal_id}:${terminal.incarnation}`]
              : current.hidden,
            activeTab:
              current.activeTab === tab ? (tabs[tabs.length - 1] ?? null) : current.activeTab,
            shellShown: current.shellShown && tabs.length > 0,
            refusedMessage: tabs.length === 0 ? null : current.refusedMessage,
          },
        },
      }
    })
  },
  selectShellTab: (runID, tab) =>
    set((s) => {
      const current = s.shellDocks[runID]
      const terminal = current?.terminals.find((item) => item.terminal_id === tab)
      if (!current || !terminal) return s
      return { shellDocks: { ...s.shellDocks, [runID]: {
        ...current, activeTab: tab, refusedMessage: null, shellShown: true,
        tabs: current.tabs.includes(tab) ? current.tabs : [...current.tabs, tab],
        hidden: current.hidden.filter((key) => key !== `${tab}:${terminal.incarnation}`),
      } } }
    }),
  setShellShown: (runID, shellShown) =>
    set((s) => ({
      shellDocks: {
        ...s.shellDocks,
        [runID]: { ...dock(s.shellDocks, runID), shellShown },
      },
    })),
  setShellRefused: (runID, message) =>
    set((s) => ({
      shellDocks: {
        ...s.shellDocks,
        [runID]: { ...dock(s.shellDocks, runID), refusedMessage: message },
      },
    })),
})
