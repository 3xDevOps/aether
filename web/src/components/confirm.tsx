import { type ReactNode, useRef, useState } from 'react'
import { toast } from 'sonner'
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
import { Callout } from '@/components/ui/callout'
import { message } from '@/lib/format'
import { useReturnFocus } from '@/lib/hooks'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

/**
 * Holds open until the server answers, and keeps its refusal on screen. Past
 * the deadline it can be closed; the request still finishes, `onDone` learns
 * whether the dialog is still open, and a failure then shows as a toast.
 */
export function Confirm({
  title,
  description,
  action,
  pending,
  onConfirm,
  onDone,
  onClose,
  children,
}: {
  title: string
  description: ReactNode
  action: string
  pending?: string
  onConfirm: () => Promise<unknown>
  onDone?: (open: boolean) => void
  onClose: () => void
  children?: ReactNode
}) {
  const [busy, setBusy] = useState(false)
  const late = usePastDeadline(busy)
  const [error, setError] = useState<string | null>(null)
  const closed = useRef(false)
  const returnFocus = useReturnFocus()
  const close = () => {
    closed.current = true
    onClose()
  }
  const confirm = async () => {
    setBusy(true)
    setError(null)
    try {
      await onConfirm()
      onDone?.(!closed.current)
      if (!closed.current) onClose()
    } catch (err) {
      if (closed.current) toast.error(`${action} failed: ${message(err)}`)
      setBusy(false)
      setError(message(err))
    }
  }
  return (
    <AlertDialog open onOpenChange={() => (!busy || late) && close()}>
      <AlertDialogContent {...returnFocus}>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        {children}
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy && !late}>{late ? 'Close' : 'Cancel'}</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              void confirm()
            }}
          >
            {late ? stillWaiting : busy ? pending ?? action : action}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
