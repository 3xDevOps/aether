import { useState } from 'react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

const modes = [
  { mode: 'tui', label: 'Standard' },
  { mode: 'acp', label: 'Enhanced' },
] as const

export function ModeSwitch({ run, agent, switchable }: { run: RunRecord; agent: AgentTerminal; switchable: boolean }) {
  const [busy, setBusy] = useState(false)
  if (run.mode !== 'tui' && run.mode !== 'acp') return null
  const live = run.status === 'running' || run.status === 'needs-attention'
  const reason = !switchable ? 'Chosen when the run starts' : !live ? 'Only a running agent can switch' : undefined
  const pending = busy || Boolean(run.switching)
  const select = async (mode: 'tui' | 'acp') => {
    if (mode === run.mode || pending || reason) return
    const held = agent.roomControl
    setBusy(true)
    try {
      const next = await api.runModeSwitch(run.id, mode, held?.has_control
        ? { control_session_id: held.control_session_id, control_generation: held.control_generation }
        : undefined)
      useStore.getState().upsertRun(next)
    } catch (err) {
      toast.error(`Switch failed: ${message(err)}`)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div role="radiogroup" aria-label="Run mode" className="flex shrink-0 items-center rounded-control border border-seam p-0.5">
      {modes.map(({ mode, label }) => {
        const checked = (run.switching ?? run.mode) === mode
        return (
          <Button
            key={mode}
            role="radio"
            aria-checked={checked}
            size="sm"
            variant={checked ? 'secondary' : 'ghost'}
            aria-disabled={Boolean(reason) || pending || undefined}
            hint={reason ?? (checked ? undefined : `Switch this run to ${label}`)}
            onClick={() => void select(mode)}
          >
            {label}
          </Button>
        )
      })}
    </div>
  )
}
