import { TriangleAlert } from 'lucide-react'
import { registerSlot, type CardSlotProps } from '@/components/slots'
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
      <button
        key={peer.run_id}
        type="button"
        onClick={() => navigate('run', { runId: peer.run_id })}
        title={`${peer.files.join('\n')}\n\nalso being changed by ${who}`}
        aria-label={`${peer.files.length} overlapping file${peer.files.length === 1 ? '' : 's'} with ${who}, open their run`}
        className="flex min-w-0 items-center gap-1 rounded-full border border-state-needs-attention/40 bg-state-needs-attention/10 px-1.5 py-0.5 text-[11px] hover:bg-state-needs-attention/20"
      >
        <TriangleAlert className="size-3 shrink-0" aria-hidden />
        <span className="max-w-32 truncate">{basename(first)}</span>
        {rest.length > 0 && <span className="shrink-0">+{rest.length}</span>}
        <span className="shrink-0 truncate" style={{ color: member?.color }}>
          {who}
        </span>
      </button>
    )
  })
}

function basename(path: string): string {
  return path.slice(path.lastIndexOf('/') + 1)
}

registerSlot('card:chips', 'conflict', ConflictChips)
