import { ArrowLeft, CircleAlert, LoaderCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useDelayed } from '@/lib/hooks'
import { useStore } from '@/store'

/**
 * What a run-detail tab shows when its run id is not in the store. Only a
 * hydrated store with a live connection can tell a deleted run from one it
 * has not read yet, so every other state has to say something weaker.
 */
export function MissingRun() {
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const navigate = useStore((s) => s.navigate)
  const unreachable = error !== null
  const loading = useDelayed(!hydrated && !unreachable)

  if (unreachable) {
    // A dead token is not an unreachable server: nothing retries, and only
    // a fresh token helps, so the tab says what the error recorded.
    return (
      <section
        aria-label="Run unavailable"
        className="flex h-full w-full items-start px-3 py-4 sm:px-4 sm:py-6"
      >
        <div className="w-full max-w-2xl border-l-2 border-state-failed bg-state-failed/10 px-3 py-3 sm:px-4">
          <div className="flex min-w-0 items-start gap-2.5">
            <CircleAlert className="mt-0.5 size-4 shrink-0 text-state-failed" aria-hidden />
            <div className="min-w-0">
              <h1 className="text-[15px] font-semibold leading-5">Run unavailable</h1>
              <p
                role="alert"
                className="mt-1 break-words whitespace-pre-wrap text-[13px] leading-5 text-muted-foreground"
              >
                {dead ? error : 'Cannot reach the server. Retrying.'}
              </p>
            </div>
          </div>
          <Button
            variant="outline"
            size="sm"
            className="mt-3"
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
      <div role="status" aria-label="Loading the run" className="w-full max-w-2xl px-3 py-4 sm:px-4 sm:py-6">
        <div className="flex items-center gap-2">
          <LoaderCircle className="size-4 animate-spin text-muted-foreground" aria-hidden />
          <p className="text-[13px] leading-5 text-muted-foreground">Loading run details...</p>
        </div>
        <div className="mt-3 grid gap-1.5">
          <Skeleton className="h-7 rounded-sm" />
          <Skeleton className="h-7 rounded-sm" />
        </div>
      </div>
    ) : null
  }

  return (
    <section
      aria-label="Run not found"
      className="flex h-full w-full items-start px-3 py-4 sm:px-4 sm:py-6"
    >
      <div className="w-full max-w-2xl border-y border-border/80 px-3 py-3 sm:px-4">
        <h1 className="text-[15px] font-semibold leading-5">Run not found</h1>
        <p className="mt-1 break-words text-[13px] leading-5 text-muted-foreground">
          This run is not on the server. It may have been deleted.
        </p>
        <Button size="sm" className="mt-3" onClick={() => navigate('board')}>
          <ArrowLeft className="size-3.5" aria-hidden />
          Back to board
        </Button>
      </div>
    </section>
  )
}
