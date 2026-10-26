import { useState } from 'react'
import type * as React from 'react'
import { actionStateWord, actionTone, useActionNames, useOpenTarget, useTargetGone } from '@/components/assistant/action-card'
import { useAssistantView } from '@/components/assistant/transcript'
import { History } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot } from '@/components/ui/status-dot'
import { actionSummary, type AssistantAction } from '@/lib/assistant-actions'
import type { AcpSession } from '@/store/sessions'

const outcomeWord = (action: AssistantAction) => [
  actionStateWord[action.state],
  action.approval === 'approved' ? 'approved by you' : action.approval === 'auto-approved' ? 'auto-approved' : undefined,
].filter(Boolean).join(' · ')

function moveFocus(event: React.KeyboardEvent<HTMLUListElement>) {
  const items = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('[data-action-entry]')]
  const at = items.indexOf(document.activeElement as HTMLButtonElement)
  const next = { ArrowDown: at + 1, ArrowUp: at - 1, Home: 0, End: items.length - 1 }[event.key]
  if (next === undefined || items.length === 0) return
  event.preventDefault()
  items[Math.max(0, Math.min(items.length - 1, next))]!.focus()
}

function Entry({ action, onOpen }: { action: AssistantAction; onOpen: () => void }) {
  const names = useActionNames()
  const open = useOpenTarget()
  const gone = useTargetGone(action.target)
  const target = action.target && !gone && (action.target.route || action.target.url) ? action.target : undefined
  return (
    <li>
      <Button
        variant="ghost"
        data-action-entry
        aria-disabled={!target}
        onClick={() => {
          if (!target) return
          onOpen()
          open(target)
        }}
        className="h-auto w-full min-w-0 items-start justify-start gap-2 px-2 py-1.5 text-left font-normal whitespace-normal text-text aria-disabled:cursor-default aria-disabled:opacity-100 coarse:h-auto coarse:min-h-11 coarse:px-2"
      >
        <span className="flex h-5 shrink-0 items-center"><StatusDot tone={actionTone[action.state]} /></span>
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="break-words text-text">{actionSummary(action, names)}</span>
          <span className="text-ui-sm text-muted">
            {outcomeWord(action)}
            {gone ? ' · no longer exists' : target ? ` · ${target.url ? 'opens the pull request' : `opens the ${target.kind === 'swarm' ? 'swarm' : target.kind}`}` : ''}
          </span>
        </span>
        <span className="flex h-5 shrink-0 items-center text-ui-sm text-muted tabular-nums"><RelativeTime at={action.at} /></span>
      </Button>
    </li>
  )
}

export function ActionHistory({ session }: { session: AcpSession | undefined }) {
  const [open, setOpen] = useState(false)
  const view = useAssistantView(session)
  const actions = [...(view?.actions ?? [])].reverse()
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="icon-sm" label="Actions">
          <History />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        aria-label="Actions in this conversation"
        className="flex w-[min(380px,calc(100vw-16px))] flex-col gap-2 p-2"
        onOpenAutoFocus={(event) => {
          const first = (event.currentTarget as HTMLElement | null)?.querySelector<HTMLElement>('[data-action-entry]')
          if (!first) return
          event.preventDefault()
          first.focus()
        }}
      >
        <p className="px-2 pt-1 text-ui-sm text-muted">Actions in this conversation, newest first</p>
        {actions.length === 0 ? (
          <p className="px-2 pb-1 text-ui-sm">No actions yet. Reads are not listed.</p>
        ) : (
          <ul aria-label="Actions" className="flex flex-col" onKeyDown={moveFocus}>
            {actions.map((action) => <Entry key={action.id} action={action} onOpen={() => setOpen(false)} />)}
          </ul>
        )}
      </PopoverContent>
    </Popover>
  )
}
