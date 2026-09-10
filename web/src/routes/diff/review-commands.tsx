import { CopyableCommand } from '@/components/copyable-command'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

/**
 * The "review it yourself" tail of the diff tab: what the last pull fetched,
 * and the two git commands that read the run branch in the linked repository,
 * ready to copy. Fetching the branch and closing the run are verbs, so they
 * live in the run action bar above with every other verb rather than a second
 * time down here - but the fetch output is an answer, not a verb, so it stays
 * where a member reviewing the branch will look for it.
 */
export function ReviewCommands({ run }: { run: RunRecord }) {
  const base = useStore((s) => s.diffs[run.id]?.base ?? '')
  const pulled = useStore((s) => s.pulls[run.id])

  if (!pulled && !run.branch) return null

  return (
    <section
      aria-label="Review locally"
      className="basis-full rounded-md border bg-card p-2.5"
    >
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
        <div>
          <h2 className="text-xs font-medium text-foreground">Review locally</h2>
          <p className="text-xs text-muted-foreground">
            Copy a command to inspect this branch in your repository.
          </p>
        </div>
        {pulled && (
          <details className="min-w-0">
            <summary className={cn(focusRing, 'cursor-pointer select-none text-xs')}>
              fetched {pulled.ref}
            </summary>
            <pre className="mt-2 max-h-48 max-w-full overflow-auto rounded-md border bg-muted/50 p-2 font-mono text-xs leading-5 whitespace-pre-wrap">
              {pulled.output}
            </pre>
          </details>
        )}
      </div>
      {run.branch && (
        <div className="mt-2 grid min-w-0 gap-1.5 md:grid-cols-2">
          <CopyableCommand command={`git log --oneline aether/${run.branch}`} />
          <CopyableCommand
            command={`git diff ${base.slice(0, 8) || 'main'}...aether/${run.branch}`}
          />
        </div>
      )}
    </section>
  )
}
