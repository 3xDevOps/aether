import { useRef, useState } from 'react'
import { toast } from 'sonner'
import { deliveryLabel } from '@/components/palette/inject-dialog'
import { Button } from '@/components/ui/button'
import { PopoverContent } from '@/components/ui/popover'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { needsYouConditions, openAction, type NeedsYouTarget, type PrimaryAction } from '@/lib/needs-you'
import type { Approval } from '@/lib/types'
import type { BoardCard } from '@/routes/board/selectors'
import { useStore } from '@/store'

const conditionOf = (card: BoardCard) => needsYouConditions.find((c) => c.id === card.needsYou)

export function cardAction(card: BoardCard, approval: Approval | undefined): PrimaryAction | undefined {
  if (card.group !== 'needs-you') return undefined
  return conditionOf(card)?.action(card.run, approval) ?? openAction
}

export function openCard(card: BoardCard, navigate: (name: string, params?: Record<string, string>) => void) {
  const { run } = card
  const target: NeedsYouTarget = conditionOf(card)?.target ?? (card.swarm ? 'swarm' : 'run')
  if (target === 'swarm' && run.mission_id) navigate('missions', { missionId: run.mission_id })
  else navigate('run', target === 'changes' ? { runId: run.id, view: 'changes' } : { runId: run.id })
}

export async function approveRequest(runID: string, approval: Approval) {
  try {
    const done = await api.approvalDecide(runID, approval.id, true)
    useStore
      .getState()
      .decideApproval(done.workspace_id, done.id, done.decision, done.decided_by ?? '', done.decided_at ?? new Date().toISOString())
    toast.success(`Approved: ${approval.action}`)
  } catch (err) {
    toast.error(`Approve failed: ${message(err)}`)
  }
}

export function CardActionButton({
  card,
  action,
  approval,
  onReply,
}: {
  card: BoardCard
  action: PrimaryAction
  approval: Approval | undefined
  onReply: () => void
}) {
  const navigate = useStore((s) => s.navigate)
  const [busy, setBusy] = useState(false)

  return (
    <Button
      variant="secondary"
      size="sm"
      data-card-action={action.kind}
      disabled={busy}
      onClick={() => {
        if (action.kind === 'approve' && approval) {
          setBusy(true)
          void approveRequest(card.run.id, approval).finally(() => setBusy(false))
        } else if (action.kind === 'reply') onReply()
        else openCard(card, navigate)
      }}
    >
      {action.label}
    </Button>
  )
}

export function ReplyComposer({
  card,
  title,
  onDone,
  returnFocus,
}: {
  card: BoardCard
  title: string
  onDone: () => void
  returnFocus: () => void
}) {
  const [text, setText] = useState('')
  const [request, setRequest] = useState<{ payload: string; key: string } | null>(null)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const leftOutside = useRef(false)

  const send = async () => {
    const payload = text.trim()
    if (!payload || sending) return
    const next = request?.payload === payload ? request : { payload, key: crypto.randomUUID() }
    setRequest(next)
    setSending(true)
    setError(null)
    try {
      const result = await api.runInject(card.run.id, payload, next.key)
      const state = result.receipt ?? result.message.state
      const label = `Message ${deliveryLabel(result).toLowerCase()}`
      if (state === 'not_sent' || state === 'uncertain') toast.error(label)
      else toast.success(label)
      onDone()
    } catch (err) {
      setError(`Send failed: ${message(err)}`)
      setSending(false)
    }
  }

  return (
    <PopoverContent
      aria-label={`Reply to ${title}`}
      className="w-[min(360px,calc(100vw-16px))]"
      onInteractOutside={() => {
        leftOutside.current = true
      }}
      onCloseAutoFocus={(event) => {
        event.preventDefault()
        if (!leftOutside.current) returnFocus()
      }}
    >
      <form
        className="flex flex-col gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          void send()
        }}
      >
        <Textarea
          autoFocus
          rows={3}
          aria-label="Message to the agent"
          placeholder="Message to the agent"
          value={text}
          onChange={(event) => setText(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
              event.preventDefault()
              void send()
            }
          }}
        />
        {error && (
          <p role="alert" className="break-words text-ui-sm text-state-failed">
            {error}
          </p>
        )}
        <div className="flex items-center justify-end gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={onDone}>
            Cancel
          </Button>
          <Button type="submit" size="sm" disabled={sending || !text.trim()}>
            {sending ? 'Sending…' : 'Send'}
          </Button>
        </div>
      </form>
    </PopoverContent>
  )
}
