import type { StreamState } from '@/lib/acp-stream'
import { modeLabel } from '@/routes/run/agent-name'
import { isTerminal, type RunRecord } from '@/store/runs'

export type Pill = 'send' | 'steer' | 'queue' | 'interrupt' | 'resume'

export function pillFor({ paused, turnRunning, steering, queueHeld, empty }: {
  paused: boolean
  turnRunning: boolean
  steering: boolean
  queueHeld: boolean
  empty: boolean
}): Pill {
  if (paused) return 'resume'
  if (!turnRunning) return 'send'
  if (empty) return 'interrupt'
  return queueHeld || !steering ? 'queue' : 'steer'
}

export const pillHint: Record<Pill, string> = {
  send: 'Starts a turn.',
  steer: 'Added to the current turn.',
  queue: 'Queued, runs after this turn.',
  interrupt: 'Stops the agent’s turn and cancels its open requests.',
  resume: 'The run is paused. Resume it to continue.',
}

export function composerBlock(run: RunRecord, maySteer: boolean, canReopen: boolean): string | null {
  if (isTerminal(run.status)) return canReopen ? 'This run has finished. Reopen it from More to message the agent.' : 'This run has finished.'
  if (run.status === 'queued' || run.status === 'provisioning') return 'The agent is still starting.'
  if (!maySteer) return run.protected ? 'This run is protected: only its owner or an admin can message the agent.' : 'You can watch this run but not message the agent.'
  return null
}

export interface EnhancedGate {
  reason: string
  interrupt?: boolean
}

export function enhancedBlock({ run, maySteer, canReopen, stream, streamError, sessionLive, pending, hasLease }: {
  run: RunRecord
  maySteer: boolean
  canReopen: boolean
  stream: StreamState | undefined
  streamError?: string
  sessionLive: boolean
  pending: number
  hasLease: boolean
}): EnhancedGate | null {
  if (run.switching) return { reason: `Switching to ${modeLabel[run.switching] ?? run.switching}…` }
  if (run.mode === 'headless') return { reason: 'Background runs take no input.' }
  const base = composerBlock(run, maySteer, canReopen)
  if (base) return { reason: base }
  if (stream === 'refused') return { reason: streamError ?? 'The session stream was refused.' }
  if (stream !== 'live') return { reason: 'Connecting to the agent…' }
  if (!sessionLive) return { reason: 'The agent’s session is not running.' }
  if (pending > 0 && hasLease) return { reason: 'Answer the request above to continue.', interrupt: true }
  return null
}
