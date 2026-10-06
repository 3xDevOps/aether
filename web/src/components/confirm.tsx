import { type ReactNode, useState } from 'react'
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

/** Holds open until the server answers, and keeps its refusal on screen. */
export function Confirm({
  title,
  description,
  action,
  onConfirm,
  onDone,
  onClose,
  children,
}: {
  title: string
  description: ReactNode
  action: string
  onConfirm: () => Promise<unknown>
  onDone: () => void
  onClose: () => void
  children?: ReactNode
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const returnFocus = useReturnFocus()
  const confirm = async () => {
    setBusy(true)
    setError(null)
    try {
      await onConfirm()
      onDone()
      onClose()
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }
  return (
    <AlertDialog open onOpenChange={() => !busy && onClose()}>
      <AlertDialogContent {...returnFocus}>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        {children}
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              void confirm()
            }}
          >
            {action}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
