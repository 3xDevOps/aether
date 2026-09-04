import type { TerminalStatusResult } from '@/lib/types'
import type { Attachment } from '@/routes/terminal/attach'
import type { SliceCreator } from '@/store/slice'

/** The server's persistent member environment and its attached tabs. */
export type EnvTerminalStatus = TerminalStatusResult

export interface EnvTerminalState {
  tabs: string[]
  activeTab: string | null
  collapsed: boolean
  status: EnvTerminalStatus | null
  statusError: string | null
}

export const initialEnvTerminal: EnvTerminalState = {
  tabs: [],
  activeTab: null,
  collapsed: false,
  status: null,
  statusError: null,
}

/** The live attachment stays outside persisted Zustand state. */
export type EnvTerminalSocket = Attachment

const sockets = new Map<string, EnvTerminalSocket>()
const listeners = new Map<string, Set<(chunk: Uint8Array) => void>>()
const ready = new Set<string>()
const pendingLines = new Map<string, string[]>()
const sentLines = new Map<string, Set<string>>()

export function hasEnvTerminalLineSent(session: string, line: string): boolean {
  return sentLines.get(session)?.has(line) ?? false
}

export function markEnvTerminalLineSent(session: string, line: string): void {
  const lines = sentLines.get(session) ?? new Set<string>()
  lines.add(line)
  sentLines.set(session, lines)
}

export function registerEnvTerminalSocket(tab: string, socket: EnvTerminalSocket): void {
  const previous = sockets.get(tab)
  if (previous && previous !== socket) {
    previous.close()
    sentLines.delete(tab)
  }
  sockets.set(tab, socket)
  ready.add(tab)
}

/** Mark a newly opened connection unavailable until its server ack arrives. */
export function setEnvTerminalSocketReady(tab: string, connected: boolean): void {
  if (connected) {
    ready.add(tab)
    const socket = sockets.get(tab)
    const lines = pendingLines.get(tab)
    if (socket && lines) {
      for (const line of lines) socket.send(line)
    }
    pendingLines.delete(tab)
    return
  }
  ready.delete(tab)
}

export function getEnvTerminalSocket(tab: string): EnvTerminalSocket | undefined {
  return sockets.get(tab)
}

export function subscribeEnvTerminalSocket(
  tab: string,
  onData: (chunk: Uint8Array) => void,
): () => void {
  const bucket = listeners.get(tab) ?? new Set<(chunk: Uint8Array) => void>()
  bucket.add(onData)
  listeners.set(tab, bucket)
  return () => {
    bucket.delete(onData)
    if (bucket.size === 0) listeners.delete(tab)
  }
}

export function emitEnvTerminalSocketData(tab: string, chunk: Uint8Array): void {
  listeners.get(tab)?.forEach((listener) => listener(chunk))
}

export function unregisterEnvTerminalSocket(tab: string): void {
  sockets.get(tab)?.close()
  sockets.delete(tab)
  ready.delete(tab)
  pendingLines.delete(tab)
  sentLines.delete(tab)
  listeners.delete(tab)
}


export interface EnvTerminalSlice {
  envTerminal: EnvTerminalState
  openEnvTerminalTab: () => string | null
  closeEnvTerminalTab: (tab: string) => void
  selectEnvTerminalTab: (tab: string) => void
  setEnvTerminalCollapsed: (collapsed: boolean) => void
  setEnvTerminalStatus: (status: EnvTerminalStatus | null, error?: string | null) => void
  resetEnvTerminal: () => void
  sendLine: (tab: string, text: string) => void
}

export const createEnvTerminalSlice: SliceCreator<EnvTerminalSlice> = (set) => ({
  envTerminal: initialEnvTerminal,
  openEnvTerminalTab: () => {
    let opened: string | null = null
    set((s) => {
      const current = s.envTerminal
      if (current.tabs.length >= 6) return s
      if (current.tabs.length === 0) {
        opened = 'main'
      } else {
        for (let n = 2; n <= 6; n++) {
          const tab = `t${n}`
          if (!current.tabs.includes(tab)) {
            opened = tab
            break
          }
        }
      }
      if (!opened) return s
      return {
        envTerminal: {
          ...current,
          tabs: [...current.tabs, opened],
          activeTab: opened,
          statusError: null,
        },
      }
    })
    return opened
  },
  closeEnvTerminalTab: (tab) => {
    if (tab === 'main') return
    unregisterEnvTerminalSocket(tab)
    set((s) => {
      const current = s.envTerminal
      if (!current.tabs.includes(tab)) return s
      const tabs = current.tabs.filter((entry) => entry !== tab)
      return {
        envTerminal: {
          ...current,
          tabs,
          activeTab: current.activeTab === tab ? (tabs[tabs.length - 1] ?? null) : current.activeTab,
        },
      }
    })
  },
  selectEnvTerminalTab: (tab) =>
    set((s) =>
      s.envTerminal.tabs.includes(tab)
        ? { envTerminal: { ...s.envTerminal, activeTab: tab } }
        : s,
    ),
  setEnvTerminalCollapsed: (collapsed) =>
    set((s) => ({ envTerminal: { ...s.envTerminal, collapsed } })),
  setEnvTerminalStatus: (status, error = null) =>
    set((s) => ({ envTerminal: { ...s.envTerminal, status, statusError: error } })),
  resetEnvTerminal: () => {
    for (const tab of sockets.keys()) unregisterEnvTerminalSocket(tab)
    pendingLines.clear()
    sentLines.clear()
    set({ envTerminal: initialEnvTerminal })
  },
  sendLine: (tab, text) => {
    const line = `${text}\n`
    const socket = getEnvTerminalSocket(tab)
    if (socket && ready.has(tab)) {
      socket.send(line)
      return
    }
    const lines = pendingLines.get(tab) ?? []
    lines.push(line)
    pendingLines.set(tab, lines)
  },
})
