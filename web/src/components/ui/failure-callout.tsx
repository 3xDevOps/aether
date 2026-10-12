import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'

/** Pass `onRetry` only when the server did not answer and a resend cannot repeat the effect. */
export function FailureCallout({ error, onRetry, onDismiss }: { error: string; onRetry?: () => void; onDismiss?: () => void }) {
  return (
    <Callout
      tone="failed"
      role="alert"
      actions={(onRetry || onDismiss) && (
        <>
          {onRetry && <Button type="button" size="sm" variant="secondary" onClick={onRetry}>Retry</Button>}
          {onDismiss && <Button type="button" size="sm" variant="ghost" onClick={onDismiss}>Dismiss</Button>}
        </>
      )}
    >
      {error}
    </Callout>
  )
}
