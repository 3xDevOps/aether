import { Button } from '@/components/ui/button'
import { CodeBlock } from '@/components/ui/code'
import { Collapsible, CollapsibleContent } from '@/components/ui/collapsible'
import { FailureCallout } from '@/components/ui/failure-callout'
import { ProgressBar } from '@/components/ui/progress-bar'
import { briefMaxBytes, byteLength } from '@/routes/memory/model'
import type { Brief } from '@/routes/memory/use-notes'

const regionID = 'memory-brief'

export function BriefToggle({ brief, open, onToggle }: { brief: Brief; open: boolean; onToggle: () => void }) {
  const size = brief.kind === 'ready' ? byteLength(brief.text) : null
  return (
    <Button variant="secondary" size="sm" aria-expanded={open} aria-controls={open ? regionID : undefined} onClick={onToggle}>
      Brief
      {size !== null && (
        <>
          {' '}
          <ProgressBar aria-hidden value={size} max={briefMaxBytes} className="w-8 bg-hover-chrome" />
          <span className="font-normal text-muted tabular-nums">
            {size.toLocaleString()} / {briefMaxBytes.toLocaleString()} B
          </span>
        </>
      )}
    </Button>
  )
}

/** What a run launched in this workspace now gets at start, byte for byte. */
export function BriefRegion({ brief, open, onRetry }: { brief: Brief; open: boolean; onRetry: () => void }) {
  return (
    <Collapsible open={open}>
      <CollapsibleContent>
        <section id={regionID} aria-label="Brief" className="flex max-h-[40dvh] min-w-0 flex-col gap-2 overflow-y-auto border-b border-seam px-4 py-3">
          <p className="text-ui-sm text-muted">What a session you launch in this workspace gets at start, byte for byte.</p>
          {brief.kind === 'loading' && <p className="text-ui text-muted">Reading the brief…</p>}
          {brief.kind === 'failed' && <FailureCallout error={brief.message} onRetry={onRetry} />}
          {brief.kind === 'ready' && (brief.text
            ? <CodeBlock className="break-words whitespace-pre-wrap">{brief.text}</CodeBlock>
            : <p className="text-ui text-muted">Empty: nothing is pinned, no session finished in the last 48 hours and there are no notes.</p>)}
        </section>
      </CollapsibleContent>
    </Collapsible>
  )
}
