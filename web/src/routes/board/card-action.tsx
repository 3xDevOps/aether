import { useRef } from 'react'
import { toast } from 'sonner'
import { create } from 'zustand'
import { deliveryLabel } from '@/components/palette/inject-dialog'
import { Button } from '@/components/ui/button'
import { PopoverContent } from '@/components/ui/popover'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { needsYouConditions, openAction, type NeedsYouTarget, type PrimaryAction } from '@/lib/needs-you'
import type { BoardCard } from '@/routes/board/selectors'
import { runRoute, type RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'
import type { Route } from '@/store/ui'

const conditionOf = (card: BoardCard) => needsYouConditions.find((c) => c.id === card.needsYou)

export function cardAction(card: BoardCard): PrimaryAction | undefined {
  if (card.group !== 'needs-you') return undefined
  return conditionOf(card)?.action(card.run) ?? openAction
}

interface Reply {
  runId: string
  text: string
  request?: { payload: string; key: string }
  /** The send in flight, which a remounted composer waits for too. */
  sending?: number
  error?: string
}

// Not card state: a group crossing `virtualizeAbove` swaps its list, which
// remounts every card in it.
export const useReply = create<{ reply: Reply | null }>(() => ({ reply: null }))

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useReply.setState({ reply: null })
})

export function setReplying(runId: string, open: boolean) {
  if (open) useReply.setState({ reply: { runId, text: '' } })
  else if (useReply.getState().reply?.runId === runId) useReply.setState({ reply: null })
}

const saveReply = (patch: Partial<Reply>) => useReply.setState(({ reply }) => ({ reply: reply && { ...reply, ...patch } }))

let sends = 0

const targetView: Partial<Record<NeedsYouTarget, RunView>> = { changes: 'changes', terminal: 'terminal' }

export function targetRoute(run: RunRecord, target: NeedsYouTarget | undefined): Route {
  return runRoute(run.id, target && targetView[target])
}

export function openCard(card: BoardCard, navigate: (name: string, params?: Record<string, string>) => void) {
  const route = targetRoute(card.run, conditionOf(card)?.target)
  navigate(route.name, route.params)
}

export function CardActionButton({
  card,
  action,
  onReply,
}: {
  card: BoardCard
  action: PrimaryAction
  onReply: () => void
}) {
  const navigate = useStore((s) => s.navigate)

  return (
    <Button
      variant="secondary"
      size="sm"
      data-card-action={action.kind}
      onClick={() => {
        if (action.kind === 'reply') onReply()
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
  const text = useReply((s) => s.reply?.text ?? '')
  const sending = useReply((s) => Boolean(s.reply?.sending))
  const error = useReply((s) => s.reply?.error)
  const leftOutside = useRef(false)

  const send = async () => {
    const reply = useReply.getState().reply
    const payload = reply?.text.trim()
    if (!reply || !payload || reply.sending) return
    const request = reply.request?.payload === payload ? reply.request : { payload, key: crypto.randomUUID() }
    const attempt = ++sends
    saveReply({ request, sending: attempt, error: undefined })
    // A reply closed or replaced meanwhile is not this send's to settle.
    const current = () => useReply.getState().reply?.sending === attempt
    try {
      const result = await api.runInject(card.run.id, payload, request.key)
      const state = result.receipt ?? result.message.state
      const label = `Message ${deliveryLabel(result).toLowerCase()}`
      if (state === 'not_sent' || state === 'uncertain') toast.error(label)
      else toast.success(label)
      if (current()) onDone()
    } catch (err) {
      if (current()) saveReply({ sending: undefined, error: `Send failed: ${message(err)}` })
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
          onChange={(event) => saveReply({ text: event.target.value })}
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
