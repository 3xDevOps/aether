import { Copy } from 'lucide-react'
import { useRef } from 'react'
import { Button } from '@/components/ui/button'
import { cn, focusRing } from '@/lib/utils'
import { copyText } from '@/lib/clipboard'

export function CopyableCommand({ command }: { command: string }) {
  const codeRef = useRef<HTMLElement>(null)

  return (
    <div className="flex min-w-0 items-center gap-1 border border-border bg-background px-1.5 py-1 rounded-sm">
      <code
        ref={codeRef}
        tabIndex={0}
        title={command}
        className={cn(
          focusRing,
          'min-w-0 flex-1 overflow-x-auto whitespace-pre px-1 font-mono text-[12px] leading-5 text-foreground select-text',
        )}
      >
        {command}
      </code>
      <Button
        hint="Copy command"
        variant="ghost"
        size="icon"
        className="size-[22px] shrink-0"
        label={`Copy ${command}`}
        onClick={() => {
          void copyText(command, codeRef.current)
        }}
      >
        <Copy className="size-3.5" aria-hidden />
      </Button>
    </div>
  )
}
