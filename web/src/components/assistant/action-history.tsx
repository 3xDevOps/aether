import { useState } from 'react'
import type * as React from 'react'
import { actionTone, stateLine, useActionNames, useBackground, useOpenTarget, useTargetGone } from '@/components/assistant/action-card'
import { useAssistantView } from '@/components/assistant/transcript'
import { History } from '@/components/icons'
import { api } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot } from '@/components/ui/status-dot'
import { actionSummary, type ActionTarget, type AssistantAction } from '@/lib/assistant-actions'
import { useStore } from '@/store'
import { loadOlderAssistant } from '@/store/assistant-stream'
import type { AcpSession } from '@/store/sessions'

const opensWord: Record<ActionTarget['kind'], string> = {
  run: 'opens the run',
  swarm: 'opens the swarm',
  pr: 'opens the pull request',
  workspace: 'opens the workspace',
  environment: 'opens the environment',
  watch: 'opens the watch',
  preference: 'opens the preference',
  link: 'opens the link',
}

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
  const gone = useTargetGone(action)
  const { background } = useBackground(action)
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
        <span className="flex h-5 shrink-0 items-center"><StatusDot tone={background ? 'working' : actionTone[action.state]} /></span>
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="break-words text-text">{actionSummary(action, names)}</span>
          <span className="text-ui-sm text-muted">
            {stateLine(action, background)}
            {gone ? ' · no longer exists' : target ? ` · ${opensWord[target.kind]}` : ''}
          </span>
        </span>
        <span className="flex h-5 shrink-0 items-center text-ui-sm text-muted tabular-nums"><RelativeTime at={action.at} /></span>
      </Button>
    </li>
  )
}

export function ActionHistory({ session }: { session: AcpSession | undefined }) {
  const [open, setOpen] = useState(false)
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
        <Actions session={session} onOpen={() => setOpen(false)} />
      </PopoverContent>
    </Popover>
  )
}

/** Mounted only while the popover is open, so a closed list reads nothing as the transcript streams. */
function Actions({ session, onOpen }: { session: AcpSession | undefined; onOpen: () => void }) {
  const view = useAssistantView(session)
  const actions = [...(view?.actions ?? [])].reverse()
  return (
    <>
      <p className="px-2 pt-1 text-ui-sm text-muted">
        {session?.more ? 'Actions in the loaded part of this conversation, newest first' : 'Actions in this conversation, newest first'}
      </p>
      {actions.length === 0 ? (
        <p className="px-2 pb-1 text-ui-sm">{session?.more ? 'No actions in the loaded messages. Reads are not listed.' : 'No actions yet. Reads are not listed.'}</p>
      ) : (
        <ul aria-label="Actions" className="flex flex-col" onKeyDown={moveFocus}>
          {actions.map((action) => <Entry key={action.id} action={action} onOpen={onOpen} />)}
        </ul>
      )}
      {session?.more && (
        <Button variant="quiet" size="sm" className="self-start px-2" disabled={session.olderLoading} onClick={() => void loadOlderAssistant(useStore, api)}>
          {session.olderLoading ? 'Loading…' : 'Show earlier actions'}
        </Button>
      )}
      {session?.olderError && <p role="alert" className="px-2 text-ui-sm text-state-failed">{session.olderError}</p>}
    </>
  )
}
