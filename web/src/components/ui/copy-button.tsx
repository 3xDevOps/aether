import type * as React from 'react'
import { Check, Copy } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { IconSwap } from '@/components/ui/text-swap'
import { copyText } from '@/lib/clipboard'
import { useTimedFlag } from '@/lib/hooks'

const copiedHold = 1800

/**
 * Copies `text` and confirms in place: the icon turns to a check and the name
 * to Copied for a moment. Without clipboard access it calls `reveal`, for a
 * `source` that clips the text, then selects `source` for a manual copy.
 */
export function CopyButton({ text, label, source, reveal }: { text: string; label: string; source: React.RefObject<HTMLElement | null>; reveal?: () => void }) {
  const [copied, confirm] = useTimedFlag(copiedHold)
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      label={copied ? 'Copied' : label}
      onClick={() => void copyText(text, () => {
        reveal?.()
        return source.current
      }, false).then((done) => done && confirm())}
    >
      <IconSwap swapped={copied} from={<Copy />} to={<Check className="text-state-done" />} />
    </Button>
  )
}
