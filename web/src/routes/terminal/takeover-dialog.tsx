import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'

interface TakeoverDialogProps {
  open: boolean
  requesterName: string
  seconds: number
  pending: boolean
  error?: string
  onDecide: (action: 'accept' | 'deny') => void
}

export function TakeoverDialog({ open, requesterName, seconds, pending, error, onDecide }: TakeoverDialogProps) {
  return (
    <AlertDialog open={open}>
      <AlertDialogContent
        onEscapeKeyDown={(event) => { event.preventDefault(); event.stopPropagation() }}
        onKeyDown={(event) => {
          if (event.repeat && (event.key === 'Enter' || event.key === ' ')) event.preventDefault()
        }}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>Terminal control requested</AlertDialogTitle>
          <AlertDialogDescription>
            {requesterName} held Take control for 5 seconds and is requesting this terminal.
            Accept to hand over control, or deny to keep it.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <p role="status" aria-live="polite" aria-atomic="true" className="text-[13px] tabular-nums">
          {seconds > 0 ? `Control transfers automatically in ${seconds} ${seconds === 1 ? 'second' : 'seconds'}.` : 'Waiting for the server’s decision…'}
        </p>
        {error && <p role="alert" className="text-[13px] text-danger-soft-foreground">{error}</p>}
        <AlertDialogFooter>
          <AlertDialogCancel aria-disabled={pending} onClick={(event) => {
            event.preventDefault()
            if (!pending) onDecide('deny')
          }}>Deny</AlertDialogCancel>
          <AlertDialogAction aria-disabled={pending} onClick={(event) => {
            event.preventDefault()
            if (!pending) onDecide('accept')
          }}>Accept</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
