import { TriangleAlert } from '@/components/icons'
import { Fragment } from 'react'
import { registerSlot, type CardSlotProps } from '@/components/slots'
import { Tooltip } from '@/components/ui/tooltip'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'

/** A finger has no hover, so on a coarse pointer the tooltip's list is also
 * written out beside the chip. */
export function ConflictChips({ run }: CardSlotProps) {
  const peers = useStore((s) => s.overlaps[run.id])
  const members = useStore((s) => s.members)
  const navigate = useStore((s) => s.navigate)
  const coarse = useMediaQuery(coarsePointer)
  if (!peers?.length) return null

  return peers.map((peer) => {
    const member = members[peer.member_id]
    const who = member?.display_name ?? peer.member_id
    const [first = '', ...rest] = peer.files
    return (
      <Fragment key={peer.run_id}>
        <Tooltip content={`${peer.files.join('\n')}\n\nalso being changed by ${who}`}>
          <button
            type="button"
            onClick={() => {
              navigate('run', { runId: peer.run_id })
            }}
            aria-label={`${peer.files.length} overlapping file${peer.files.length === 1 ? '' : 's'} with ${who}, open their run`}
            className={cn(
              focusRing,
              'inline-flex min-h-[22px] coarse:min-h-11 min-w-0 max-w-full items-center gap-1.5 border border-state-needs-you/40 bg-state-needs-you/10 px-1.5 text-ui-sm hover:bg-state-needs-you/20',
            )}
          >
            <TriangleAlert className="size-3.5 shrink-0 text-state-needs-you" aria-hidden />
            <span className="max-w-32 truncate font-code">{basename(first)}</span>
            {rest.length > 0 && (
              <span className="shrink-0 text-muted">+{rest.length}</span>
            )}
            <span className="max-w-28 shrink-0 truncate" style={{ color: member?.color }}>
              {who}
            </span>
          </button>
        </Tooltip>
        {coarse && (
          <span className="min-w-0 basis-full break-all font-code text-ui-xs text-muted">
            {peer.files.join(' ')} - also being changed by {who}
          </span>
        )}
      </Fragment>
    )
  })
}

function basename(path: string): string {
  return path.slice(path.lastIndexOf('/') + 1)
}

function ConflictWarning({ run }: CardSlotProps) {
  const peers = useStore((s) => s.overlaps[run.id])
  if (!peers?.length) return null
  const label = `Files also changed by ${peers.length} other run${peers.length === 1 ? '' : 's'}`
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="link" size="sm" hint={label}>
          <TriangleAlert />
          {peers.length} {peers.length === 1 ? 'overlap' : 'overlaps'}
        </Button>
      </PopoverTrigger>
      <PopoverContent aria-label="File overlap warnings">
        <p className="mb-2 font-medium">Overlapping files</p>
        <div className="flex flex-wrap items-center gap-2">
          <ConflictChips run={run} />
        </div>
      </PopoverContent>
    </Popover>
  )
}

registerSlot('card:meta', 'conflict', ConflictWarning)
