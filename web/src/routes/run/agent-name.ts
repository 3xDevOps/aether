import { useEffect, useSyncExternalStore } from 'react'
import { api, type Api } from '@/lib/api'

let names: Record<string, string> | null = null
let request: Promise<void> | null = null
const listeners = new Set<() => void>()

function load(client: Api) {
  request ??= client.agentList().then(
    (agents) => {
      names = Object.fromEntries(agents.map((agent) => [agent.name, agent.display_name || agent.name]))
      listeners.forEach((listener) => listener())
    },
    () => {
      request = null
    },
  )
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

export function useAgentName(harness: string, client: Api = api): string {
  const known = useSyncExternalStore(subscribe, () => names, () => names)
  useEffect(() => {
    if (!names) load(client)
  }, [client])
  return known?.[harness] ?? harness
}

export const modeLabel: Record<string, string> = {
  tui: 'Standard',
  acp: 'Enhanced',
  headless: 'Background',
}
