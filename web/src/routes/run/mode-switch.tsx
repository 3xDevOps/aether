import { useEffect, useState } from 'react'
import type * as React from 'react'
import { toast } from 'sonner'
import { ArrowRightLeft } from '@/components/icons'
import type { ExtraItem } from '@/components/run-actions'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { api, ApiError } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { AgentInfo } from '@/lib/types'
import { modeLabel } from '@/routes/run/agent-name'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

type Refusal = 'session_not_reported' | 'adapter_not_installed' | 'not_switchable'

const refusals = new Set<unknown>(['session_not_reported', 'adapter_not_installed', 'not_switchable'] satisfies Refusal[])

function refusalOf(err: unknown): Refusal | undefined {
  const reason = err instanceof ApiError ? (err.data as { reason?: unknown } | undefined)?.reason : undefined
  return refusals.has(reason) ? (reason as Refusal) : undefined
}

const whatHappens: Record<'tui' | 'acp', string> = {
  acp: 'The agent restarts in Enhanced mode with the same conversation. You follow and answer it in the Session view instead of its terminal.',
  tui: 'The agent restarts in its own terminal with the same conversation. You answer it in the Terminal view from then on.',
}

export function useModeSwitch(run: RunRecord, agent: AgentTerminal, entry: AgentInfo | undefined): { item: ExtraItem | null; dialog: React.ReactNode } {
  const navigate = useStore((s) => s.navigate)
  const [confirming, setConfirming] = useState(false)
  const [refused, setRefused] = useState<Refusal>()
  useEffect(() => setRefused(undefined), [run.status])

  const live = run.status === 'running' || run.status === 'needs-attention'
  if (!entry?.switchable || refused === 'not_switchable' || !live || (run.mode !== 'tui' && run.mode !== 'acp')) {
    return { item: null, dialog: null }
  }
  const target = run.mode === 'tui' ? 'acp' : 'tui'
  const label = `Switch to ${modeLabel[target]}…`
  const switchTo = async () => {
    const held = agent.roomControl
    try {
      const next = await api.runModeSwitch(run.id, target, held?.has_control
        ? { control_session_id: held.control_session_id, control_generation: held.control_generation }
        : undefined)
      useStore.getState().upsertRun(next)
    } catch (err) {
      setRefused(refusalOf(err))
      toast.error(`Switch failed: ${errorSentence(err)}`)
    }
  }

  const base = { id: 'mode-switch', label, Icon: ArrowRightLeft }
  let item: ExtraItem
  if (run.switching) item = { ...base, disabled: true, description: `Switching to ${modeLabel[run.switching]}…`, onSelect: () => {} }
  else if (target === 'acp' && (entry.enhanced_installed === false || refused === 'adapter_not_installed')) {
    item = { ...base, description: 'Enhanced adapter not installed · Set up', onSelect: () => navigate('agents') }
  } else if (refused === 'session_not_reported') {
    item = { ...base, disabled: true, description: 'Available after the agent’s first turn', onSelect: () => {} }
  } else item = { ...base, onSelect: () => setConfirming(true) }

  const dialog = confirming && (
    <AlertDialog open onOpenChange={(open) => { if (!open) setConfirming(false) }}>
      <AlertDialogContent className="max-w-[min(420px,calc(100%-2rem))]">
        <AlertDialogHeader>
          <AlertDialogTitle>Switch to {modeLabel[target]}?</AlertDialogTitle>
          <AlertDialogDescription>{whatHappens[target]}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction variant="primary" onClick={() => void switchTo()}>Switch to {modeLabel[target]}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
  return { item, dialog }
}
