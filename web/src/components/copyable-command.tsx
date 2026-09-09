import { Copy } from 'lucide-react'
import { useRef } from 'react'
import { Button } from '@/components/ui/button'
import { copyText } from '@/lib/clipboard'

/** One exact command, shown as it must be typed, with a button that copies it. */
export function CopyableCommand({ command }: { command: string }) {
  const codeRef = useRef<HTMLElement>(null)

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
        onClick={() => void copyText(command, codeRef.current)}
      >
        <Copy className="size-3" aria-hidden />
      </Button>
    </div>
  )
}
