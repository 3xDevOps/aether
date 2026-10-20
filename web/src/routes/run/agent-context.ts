import { createContext, useContext } from 'react'
import type { AgentTerminal } from '@/routes/run/agent-terminal'

/** The open run's agent, for surfaces inside the run view that send it a message. */
export const RunAgentContext = createContext<{ agent: AgentTerminal; agentName: string } | null>(null)

export function useRunAgent() {
  return useContext(RunAgentContext)
}
