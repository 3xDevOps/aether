import { Copy } from 'lucide-react'
import { useRef } from 'react'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/heroui'
import { copyText } from '@/lib/clipboard'

/** One exact command, shown as it must be typed, with a button that copies it. */
export function CopyableCommand({ command }: { command: string }) {
  const codeRef = useRef<HTMLElement>(null)

  return (
    <div className="flex min-w-0 items-center gap-1 border border-border bg-background px-1.5 py-1 rounded-sm">
      <code
        ref={codeRef}
        title={command}
        className="min-w-0 flex-1 overflow-x-auto whitespace-pre px-1 font-mono text-[12px] leading-5 text-foreground select-text"
      >
        {command}
      </code>
      <Tooltip>
        <Tooltip.Trigger<'button'>
          render={(triggerProps) => (
            <Button
              {...triggerProps}
              variant="ghost"
              size="icon"
              className="size-[22px] shrink-0"
              aria-label={`Copy ${command}`}
              onClick={() => {
                void copyText(command, codeRef.current)
              }}
            >
              <Copy className="size-3.5" aria-hidden />
            </Button>
          )}
        />
        <Tooltip.Content>Copy command</Tooltip.Content>
      </Tooltip>
    </div>
  )
}
