import { memo, useState } from 'react'
import { Pencil, Pin, PinOff, Trash2 } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { CardControls, CardTitle } from '@/components/ui/card'
import { RelativeTime } from '@/components/ui/relative-time'
import { IconSwap } from '@/components/ui/text-swap'
import type { MemoryItem } from '@/lib/types'
import { cn } from '@/lib/utils'
import { kindLabel, mayChange, mayPinForSelf, sourceLabel, superseded } from '@/routes/memory/model'
import { Byline, PinBadge, PRLink, RunLink } from '@/routes/memory/note'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'

const dot = <span aria-hidden>·</span>
// A phone has no room for the run or pull request link; a tap opens the detail, which lists both.
const phoneHidden = <span aria-hidden className="max-sm:hidden">·</span>

/**
 * One note in two lines: the title, then kind, source, author, the pull
 * request or else the run, and the age. A member who may write gets Pin or
 * Unpin, Replace and Remove on hover or focus, except on another member's
 * member note; a finger opens the detail, which has every action.
 */
export const NoteRow = memo(function NoteRow({
  item,
  agents,
  canWrite,
  onOpen,
  onLook,
  onPin,
  onReplace,
  onRemove,
}: {
  item: MemoryItem
  agents: Record<string, string>
  canWrite: boolean
  onOpen: (item: MemoryItem) => void
  onLook: (item: MemoryItem | null) => void
  onPin: (item: MemoryItem) => Promise<void>
  onReplace: (item: MemoryItem) => void
  onRemove: (item: MemoryItem) => void
}) {
  const [pinning, setPinning] = useState(false)
  const self = useSelf()
  const mergedInto = useStore((s) => s.mergedInto)
  const writable = canWrite && mayChange(item, self, mergedInto)
  const replaced = superseded(item)
  const pinned = Boolean(item.pinned_at)
  const pinLabel = pinned ? 'Unpin' : item.scope === 'member' ? 'Pin for your new sessions' : 'Pin for every new session'

  const togglePin = async () => {
    setPinning(true)
    try {
      await onPin(item)
    } finally {
      setPinning(false)
    }
  }

  return (
    <article
      onPointerEnter={(e) => e.pointerType !== 'touch' && onLook(item)}
      onPointerLeave={() => onLook(null)}
      onFocus={() => onLook(item)}
      onBlur={(e) => !e.currentTarget.contains(e.relatedTarget as Node | null) && onLook(null)}
      className={cn(
        'group/row relative flex min-w-0 flex-col gap-0.5 rounded-control px-2.5 pt-2.5 pb-2.25',
        'transition-colors duration-hover motion-reduce:transition-none hover:bg-hover focus-within:bg-hover',
      )}
    >
      <CardTitle onOpen={() => onOpen(item)} className={cn('font-normal [&>button]:after:rounded-control', replaced && 'text-muted')}>
        {item.title}
      </CardTitle>
      <div className={cn('flex h-5 min-w-0 items-center gap-1.5 text-ui-sm text-muted', writable && 'pointer-fine:pr-24')}>
        <span className="min-w-0 shrink truncate">
          {kindLabel[item.kind]} · {sourceLabel[item.source]} · <Byline item={item} agents={agents} />
        </span>
        {item.pr_url ? (
          <>
            {phoneHidden}
            <CardControls className="shrink-0 max-sm:hidden"><PRLink item={item} className="whitespace-nowrap" /></CardControls>
          </>
        ) : item.run_id && (
          <>
            {phoneHidden}
            <CardControls className="max-w-80 shrink-[2] max-sm:hidden"><RunLink id={item.run_id} className="block min-w-0 shrink truncate" /></CardControls>
          </>
        )}
        {dot}
        <RelativeTime at={item.created_at} compact className="shrink-0 tabular-nums" />
        {pinned && <PinBadge item={item} />}
        {replaced && <Badge tone="closed">Superseded</Badge>}
      </div>
      {writable && (
        <CardControls
          className={cn(
            'absolute right-1.5 bottom-1.5 gap-0.5 opacity-0 transition-opacity duration-100 motion-reduce:transition-none coarse:hidden',
            'group-hover/row:opacity-100 group-hover/row:delay-50 group-focus-within/row:opacity-100',
          )}
        >
          {!replaced && (pinned || item.scope !== 'member' || mayPinForSelf(item, self, mergedInto)) && (
            <Button variant="ghost" size="icon-sm" label={pinLabel} disabled={pinning} onClick={() => void togglePin()}>
              <IconSwap swapped={pinned} from={<Pin />} to={<PinOff />} />
            </Button>
          )}
          {!replaced && (
            <Button variant="ghost" size="icon-sm" label="Replace" onClick={() => onReplace(item)}>
              <Pencil />
            </Button>
          )}
          <Button variant="ghost" size="icon-sm" label="Remove" className="hover:not-aria-disabled:text-state-failed" onClick={() => onRemove(item)}>
            <Trash2 />
          </Button>
        </CardControls>
      )}
    </article>
  )
})
