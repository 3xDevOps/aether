import { Upload } from '@/components/icons'
import { TextSwap } from '@/components/ui/text-swap'
import { fitLabel, useFileDrop } from '@/lib/file-drag'
import { cn, focusRing } from '@/lib/utils'

const sheet = 'absolute top-1 h-8 w-6.5 rounded-chip border border-seam bg-raised shadow-card transition-[translate,rotate] duration-glide ease-glide motion-reduce:transition-none'

/** Reads `data-drag` and `:hover` from an ancestor marked `group/zone`. */
export function DropIllustration() {
  return (
    <span aria-hidden className="relative flex h-9 w-14 shrink-0 justify-center">
      <span className={cn(sheet, 'left-1.5 -rotate-8 group-data-drag/zone:-translate-x-3.5 group-data-drag/zone:-translate-y-0.5 group-data-drag/zone:-rotate-12')} />
      <span className={cn(sheet, 'right-1.5 rotate-8 group-data-drag/zone:translate-x-3.5 group-data-drag/zone:-translate-y-0.5 group-data-drag/zone:rotate-12')} />
      <span className="relative z-10 flex h-full w-8 items-center justify-center rounded-control border border-seam bg-raised shadow-card transition-[translate] duration-glide ease-glide group-[:hover:not(:disabled)]/zone:-translate-y-0.5 group-data-drag/zone:-translate-y-1.5 motion-reduce:transition-none">
        <Upload className="size-3.5 text-muted transition-colors group-[:hover:not(:disabled)]/zone:text-text group-data-drag/zone:animate-bob group-data-drag/zone:text-accent motion-reduce:transition-none motion-reduce:group-data-drag/zone:animate-none" />
      </span>
    </span>
  )
}

export function DropZone({
  onBrowse,
  onFiles,
  limit,
  blocked,
  hint,
}: {
  onBrowse: () => void
  onFiles: (files: File[]) => void
  limit: number
  blocked?: string
  hint: string
}) {
  const drop = useFileDrop({ onFiles, limit })
  const dragging = drop.count !== null && !blocked
  const headline = blocked ?? (
    drop.count === null
      ? <>Drag files here or <span className="text-accent">browse</span></>
      : drop.refused
        ? fitLabel(limit)
        : `Drop to attach ${drop.count} ${drop.count === 1 ? 'file' : 'files'}`
  )
  const headlineKey = blocked ? 'blocked' : drop.count === null ? 'rest' : drop.refused ? 'refused' : `drag-${drop.count}`
  return (
    <button
      type="button"
      data-slot="drop-zone"
      data-drag={dragging ? '' : undefined}
      disabled={Boolean(blocked)}
      onClick={onBrowse}
      {...drop.handlers}
      className={cn(
        'group/zone flex w-full flex-col items-center gap-3 rounded-panel border border-dashed border-control/60 bg-chrome px-3 pt-5 pb-4 text-center transition-colors duration-hover',
        'hover:not-disabled:border-control hover:not-disabled:bg-hover-chrome data-drag:border-accent data-drag:bg-accent-soft',
        'disabled:cursor-not-allowed disabled:border-seam motion-reduce:transition-none',
        focusRing,
      )}
    >
      <DropIllustration />
      <span className="flex flex-col items-center">
        <TextSwap swapKey={headlineKey} mode="crossfade" className={cn('justify-items-center text-ui font-medium', blocked ? 'text-muted' : 'text-text')}>
          {headline}
        </TextSwap>
        <span className="text-ui-sm text-muted">{hint}</span>
      </span>
    </button>
  )
}
