import type { RefObject } from 'react'
import { setDictationState, useDictationState } from '@/components/dictation/state'
import { TriangleAlert, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import { cn } from '@/lib/utils'

const literal = /((?:sudo )?aether-server dictation install|\/[\w.@-]+(?:\/[\w.@-]+)+)/

/** The last dictation failure or empty result for `textarea`, verbatim, above the box it belongs to. */
export function DictationNotice({ textarea, className }: { textarea: RefObject<HTMLTextAreaElement | null>; className?: string }) {
  const { notice } = useDictationState(textarea)
  if (!notice) return null
  const parts = notice.text.split(literal)
  return (
    <div
      role={notice.error ? 'alert' : 'status'}
      data-slot="dictation-notice"
      className={cn('flex items-start gap-2 rounded-control py-1.5 pr-1 pl-2.5 text-ui-sm', notice.error ? 'bg-state-failed-soft text-text' : 'bg-chrome text-muted', className)}
    >
      {notice.error && <TriangleAlert aria-hidden className="mt-0.5 size-3.5 shrink-0 text-state-failed" />}
      <p className="min-w-0 flex-1 py-0.5 break-words whitespace-pre-line select-text">
        {parts.map((part, i) => (i % 2 ? <Code key={i}>{part}</Code> : part))}
      </p>
      <Button type="button" size="icon-sm" variant="ghost" label="Dismiss" onClick={() => setDictationState(textarea, { notice: null })}>
        <X />
      </Button>
    </div>
  )
}
