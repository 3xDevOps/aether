import { ToggleGroup } from 'radix-ui'
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
  if (!switchable || (run.mode !== 'tui' && run.mode !== 'acp')) return null
  const live = run.status === 'running' || run.status === 'needs-attention'
  const reason = live ? undefined : 'Only a running agent can switch'
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
  const current = run.switching ?? run.mode
  return (
    <ToggleGroup.Root
      type="single"
      aria-label="Run mode"
      value={current}
      onValueChange={(next) => {
        if (next === 'tui' || next === 'acp') void select(next)
      }}
      className="flex shrink-0 items-center rounded-control border border-seam p-0.5"
    >
      {modes.map(({ mode, label }) => (
        <ToggleGroup.Item key={mode} value={mode} asChild>
          <Button
            size="sm"
            variant={current === mode ? 'secondary' : 'ghost'}
            aria-disabled={Boolean(reason) || pending || undefined}
            hint={reason ?? (current === mode ? undefined : `Switch this run to ${label}`)}
          >
            {label}
          </Button>
        </ToggleGroup.Item>
      ))}
    </ToggleGroup.Root>
  )
}
