import { CopyableCommand } from '@/components/copyable-command'
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

  return (
    <>
      {pulled && (
        <details className="basis-full">
          <summary className="cursor-pointer select-none">
            fetched {pulled.ref}
          </summary>
          <pre className="mt-1 max-h-48 overflow-auto rounded-md border bg-muted/50 p-2 font-mono text-[11px] whitespace-pre-wrap">
            {pulled.output}
          </pre>
        </details>
      )}
      {run.branch && (
        <div className="basis-full space-y-1">
          <CopyableCommand command={`git log --oneline aether/${run.branch}`} />
          <CopyableCommand
            command={`git diff ${base.slice(0, 8) || 'main'}...aether/${run.branch}`}
          />
        </div>
      )}
    </>
  )
}
