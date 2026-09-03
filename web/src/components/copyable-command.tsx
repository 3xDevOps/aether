import { Copy } from 'lucide-react'
import { useRef } from 'react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'

/**
 * One exact command, shown as it must be typed, with a button that copies
 * it. jsdom, plain-http origins and older engines have no
 * navigator.clipboard; the fallback selects the command text for a manual
 * copy (same pattern as the members InviteDialog).
 */
export function CopyableCommand({ command }: { command: string }) {
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
