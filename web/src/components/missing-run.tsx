import { ArrowLeft, CircleAlert, LoaderCircle } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useDelayed } from '@/lib/hooks'
import { useKeybindings } from '@/lib/keybindings'
import { useStore } from '@/store'

/** Only a hydrated store with a live connection can tell a deleted run from an unread one. */
export function MissingRun() {
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const navigate = useStore((s) => s.navigate)
  useKeybindings('run', { 'leave-run': () => navigate('board') })
  const unreachable = error !== null
  const loading = useDelayed(!hydrated && !unreachable)

  if (unreachable) {
    // A dead token is not an unreachable server: nothing retries, so show the recorded error.
    return (
      <section
        aria-label="Run unavailable"
        className="flex h-full min-w-0 w-full items-start px-3 py-3 sm:px-4 sm:py-4"
      >
        <div className="min-w-0 w-full max-w-3xl border-l-2 border-state-failed bg-state-failed/10 px-3 py-2.5 sm:px-4">
          <div className="flex min-w-0 items-start gap-2.5">
            <CircleAlert className="mt-0.5 size-4 shrink-0 text-state-failed" aria-hidden />
            <div className="min-w-0">
              <h1 className="text-title leading-5">Run unavailable</h1>
              <p
                role="alert"
                className="mt-0.5 break-words whitespace-pre-wrap text-ui leading-5 text-muted"
              >
                {dead ? error : 'Cannot reach the server. Retrying.'}
              </p>
            </div>
          </div>
          <Button
            variant="secondary"
            size="sm"
            className="mt-2"
            onClick={() => navigate('board')}
          >
            <ArrowLeft className="size-3.5" aria-hidden />
            Back to board
          </Button>
        </div>
      </section>
    )
  }

  if (!hydrated) {
    return loading ? (
      <div
        role="status"
        aria-label="Loading the run"
        className="w-full max-w-3xl px-3 py-3 sm:px-4 sm:py-4"
      >
        <div className="flex items-center gap-2">
          <LoaderCircle className="size-4 animate-spin text-muted" aria-hidden />
          <p className="text-ui leading-5 text-muted">Loading run details…</p>
        </div>
        <div className="mt-2 grid gap-1">
          <div className="h-7"><Skeleton className="size-full" /></div>
          <div className="h-7"><Skeleton className="size-full" /></div>
        </div>
      </div>
    ) : null
  }

  return (
    <section
      aria-label="Run not found"
      className="flex h-full min-w-0 w-full items-start px-3 py-3 sm:px-4 sm:py-4"
    >
      <div className="min-w-0 w-full max-w-3xl border-y border-seam/80 px-3 py-2.5 sm:px-4">
        <h1 className="text-title leading-5">Run not found</h1>
        <p className="mt-0.5 break-words text-ui leading-5 text-muted">
          This run is not on the server. It may have been deleted.
        </p>
        <Button size="sm" className="mt-2" onClick={() => navigate('board')}>
          <ArrowLeft className="size-3.5" aria-hidden />
          Back to board
        </Button>
      </div>
    </section>
  )
}
