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
    // A dead token is not an unreachable server: nothing retries, and only a
    // fresh token helps, so the tab says what the error recorded.
    return (
      <section
        aria-label="Run unavailable"
        className="mx-auto flex h-full w-full max-w-xl items-center justify-center p-6"
      >
        <div className="w-full rounded-lg border border-state-failed/40 bg-state-failed/10 p-5">
          <div className="flex items-start gap-3">
            <CircleAlert className="mt-0.5 size-5 shrink-0 text-state-failed" aria-hidden />
            <div>
              <h1 className="text-base font-semibold">Run unavailable</h1>
              <p role="alert" className="mt-1 text-sm leading-6 text-muted-foreground">
                {dead ? error : 'Cannot reach the server. Retrying.'}
              </p>
            </div>
          </div>
          <Button
            variant="outline"
            size="sm"
            className="mt-4"
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
      <div role="status" aria-label="Loading the run" className="mx-auto w-full max-w-xl space-y-3 p-6">
        <div className="flex items-center gap-2">
          <LoaderCircle className="size-4 animate-spin text-muted-foreground" aria-hidden />
          <p className="text-sm text-muted-foreground">Loading run details...</p>
        </div>
        <Skeleton className="h-24 w-full rounded-lg" />
      </div>
    ) : null
  }

  return (
    <section
      aria-label="Run not found"
      className="mx-auto flex h-full w-full max-w-xl items-center justify-center p-6"
    >
      <div className="w-full rounded-lg border bg-card p-5">
        <h1 className="text-base font-semibold">Run not found</h1>
        <p className="mt-1 text-sm leading-6 text-muted-foreground">
          This run is not on the server. It may have been deleted.
        </p>
        <Button size="sm" className="mt-4" onClick={() => navigate('board')}>
          <ArrowLeft className="size-3.5" aria-hidden />
          Back to board
        </Button>
      </div>
    </section>
  )
}
