import { TriangleAlert } from 'lucide-react'
import { registerSlot, type CardSlotProps } from '@/components/slots'
import { Tooltip } from '@/components/ui/heroui'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'

/**
 * The conflict radar's front side (): the other active runs touching
 * files this run also touches. Advisory only - nothing here blocks or queues
 * anything. Each chip names a file and the member on the other side, and
 * takes you to their run.
 */
export function ConflictChips({ run }: CardSlotProps) {
  const peers = useStore((s) => s.overlaps[run.id])
  const members = useStore((s) => s.members)
  const navigate = useStore((s) => s.navigate)
  if (!peers?.length) return null

  return peers.map((peer) => {
    const member = members[peer.member_id]
    const who = member?.display_name ?? peer.member_id
    const [first = '', ...rest] = peer.files
    return (
      <Tooltip key={peer.run_id}>
        <Tooltip.Trigger<'button'>
          render={(triggerProps) => (
            <button
              {...triggerProps}
              type="button"
              onClick={() => {
                navigate('terminal', { runId: peer.run_id })
              }}
              aria-label={`${peer.files.length} overlapping file${peer.files.length === 1 ? '' : 's'} with ${who}, open their run`}
              className={cn(
                focusRing,
                'inline-flex min-w-0 items-center gap-1.5 rounded-md border border-state-needs-attention/40 bg-state-needs-attention/10 px-2 py-1 text-xs hover:bg-state-needs-attention/20',
              )}
            >
              <TriangleAlert className="size-3.5 shrink-0 text-state-needs-attention" aria-hidden />
              <span className="max-w-32 truncate font-mono">{basename(first)}</span>
              {rest.length > 0 && (
                <span className="shrink-0 text-muted-foreground">+{rest.length}</span>
              )}
              <span className="max-w-28 shrink-0 truncate" style={{ color: member?.color }}>
                {who}
              </span>
            </button>
          )}
        />
        <Tooltip.Content className="whitespace-pre-line">
          {`${peer.files.join('\n')}\n\nalso being changed by ${who}`}
        </Tooltip.Content>
      </Tooltip>
    )
  })
}

function basename(path: string): string {
  return path.slice(path.lastIndexOf('/') + 1)
}

registerSlot('card:chips', 'conflict', ConflictChips)
