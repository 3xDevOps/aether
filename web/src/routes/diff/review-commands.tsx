import { Copy } from 'lucide-react'
import { useRef } from 'react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

/**
 * The "review it yourself" tail of the diff tab: the two git commands that
 * read the run branch in the linked repository, ready to copy. Fetching the
 * branch and closing the run are verbs, so they live in the run action bar
 * above with every other verb rather than a second time down here.
 */
export function ReviewCommands({ run }: { run: RunRecord }) {
  const base = useStore((s) => s.diffs[run.id]?.base ?? '')
  if (!run.branch) return null

  return (
    <div className="basis-full space-y-1">
      <CopyableCommand command={`git log --oneline aether/${run.branch}`} />
      <CopyableCommand
        command={`git diff ${base.slice(0, 8) || 'main'}...aether/${run.branch}`}
      />
    </div>
  )
}

/**
 * One copyable review command: the text, and a button that clipboards it.
 * jsdom, plain-http origins and older engines have no navigator.clipboard;
 * the fallback selects the command text for a manual copy (same pattern as
 * the members InviteDialog).
 */
function CopyableCommand({ command }: { command: string }) {
  const codeRef = useRef<HTMLElement>(null)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      toast.success('Copied')
    } catch {
      const node = codeRef.current
      if (!node) return
      const range = document.createRange()
      range.selectNodeContents(node)
      const selection = window.getSelection()
      selection?.removeAllRanges()
      selection?.addRange(range)
    }
  }

  return (
    <div className="flex items-center gap-1">
      <code
        ref={codeRef}
        className="min-w-0 truncate rounded bg-muted/50 px-1.5 py-0.5 font-mono text-[11px]"
      >
        {command}
      </code>
      <Button
        variant="ghost"
        size="icon"
        className="size-5"
        aria-label={`Copy ${command}`}
        onClick={() => void copy()}
      >
        <Copy className="size-3" aria-hidden />
      </Button>
    </div>
  )
}
