import { Copy } from 'lucide-react'
import { useRef } from 'react'
import { Button } from '@/components/ui/button'
import { copyText } from '@/lib/clipboard'

/** One exact command, shown as it must be typed, with a button that copies it. */
export function CopyableCommand({ command }: { command: string }) {
  const codeRef = useRef<HTMLElement>(null)

  return (
    <div className="flex min-w-0 items-center gap-2 rounded-md border border-border/70 bg-muted/30 px-2 py-1.5">
      <code
        ref={codeRef}
        title={command}
        className="min-w-0 flex-1 overflow-x-auto whitespace-pre rounded-sm px-1 font-mono text-[12px] leading-5 text-foreground"
      >
        {command}
      </code>
      <Button
        variant="ghost"
        size="icon"
        className="size-8 shrink-0"
        aria-label={`Copy ${command}`}
        title="Copy command"
        onClick={() => void copyText(command, codeRef.current)}
      >
        <Copy className="size-3.5" aria-hidden />
      </Button>
    </div>
  )
}
