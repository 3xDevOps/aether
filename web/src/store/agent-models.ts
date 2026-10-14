import type { AgentModelsResult } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface AgentModelsState {
  result?: AgentModelsResult
  loading?: boolean
  /** The agent.models call itself failed; `result.error` is a discovery the server reported. */
  error?: string
}

export interface AgentModelsSlice {
  agentModels: Record<string, AgentModelsState>
  setAgentModels: (key: string, state: AgentModelsState) => void
}

export const createAgentModelsSlice: SliceCreator<AgentModelsSlice> = (set) => ({
  agentModels: {},
  setAgentModels: (key, state) => set((s) => ({ agentModels: { ...s.agentModels, [key]: state } })),
})
