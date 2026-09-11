import { CopyableCommand } from '@/components/copyable-command'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/**
 * The "review it yourself" tail of the diff tab: what the last pull fetched,
 * and the two git commands that read the run branch in the linked repository,
 * ready to copy. Fetching the branch and closing the run are verbs, so they
 * live in the run action bar above with every other verb rather than a second
 * time down here - but the fetch output is an answer, not a verb, so it stays
 * where a member reviewing the branch will look for it.
 *
 * All of it is about a repository on this machine, so it is gated on the
 * same `pull` verb that fetches into one. A gateway without it - a phone on
 * the server's dashboard - has no repository to copy these into.
 */
export function ReviewCommands({ run }: { run: RunRecord }) {
  const base = useStore((s) => s.diffs[run.id]?.base ?? '')
  const pulled = useStore((s) => s.pulls[run.id])
  const cap = useCapability()

  if (!cap.hasLocal('pull')) return null
  if (!pulled && !run.branch) return null

  return (
    <section
      aria-label="Review locally"
      className="min-w-0 border-b px-3 py-2"
    >
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-x-3 gap-y-1">
        <div className="min-w-0">
          <h2 className="text-[12px] font-medium text-foreground">Review locally</h2>
          <p className="text-[12px] text-muted-foreground">
            Copy a command to inspect this branch in your repository.
          </p>
        </div>
        {pulled && (
          <Collapsible className="min-w-0 max-w-full">
            <CollapsibleTrigger className="max-w-full truncate text-[12px] text-muted-foreground hover:text-foreground">
              fetched {pulled.ref}
            </CollapsibleTrigger>
            <CollapsibleContent>
              <pre className="mt-2 max-h-48 max-w-full overflow-auto border bg-sidebar p-2 font-mono text-[12px] leading-5 whitespace-pre-wrap">
                {pulled.output}
              </pre>
            </CollapsibleContent>
          </Collapsible>
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
