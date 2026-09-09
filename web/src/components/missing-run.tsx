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
      <p className="p-4 text-sm text-muted-foreground">
        {dead ? error : 'Cannot reach the server. Retrying.'}
      </p>
    )
  }

  if (!hydrated) {
    return loading ? (
      <div role="status" aria-label="Loading the run" className="space-y-2 p-4">
        <Skeleton className="h-4 w-64" />
        <Skeleton className="h-4 w-40" />
      </div>
    ) : null
  }

  return (
    <div className="space-y-3 p-4">
      <p className="text-sm text-muted-foreground">
        This run is not on the server. It may have been deleted.
      </p>
      <Button size="sm" onClick={() => navigate('board')}>
        Back to board
      </Button>
    </div>
  )
}
