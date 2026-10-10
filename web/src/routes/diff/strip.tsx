import { useRef, useState } from 'react'
import { ChevronDown, Ellipsis, History, RefreshCw, WrapText } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  Menu,
  MenuContent,
  MenuItem,
  MenuLabel,
  MenuRadioGroup,
  MenuRadioItem,
  MenuSeparator,
  MenuTrigger,
} from '@/components/ui/menu'
import { RelativeTime } from '@/components/ui/relative-time'
import { cn } from '@/lib/utils'
import { ConflictChips } from '@/routes/diff/conflict-chips'
import type { PatchFile } from '@/routes/diff/parse'
import { Counts } from '@/routes/diff/patch-view'
import { PublishDialog } from '@/routes/diff/publish-dialog'
import { ReviewLocallyDialog, useCanReviewLocally } from '@/routes/diff/review-commands'
import { useCapability } from '@/store/hooks'
import type { DiffSnapshot } from '@/store/diff'
import type { RunRecord } from '@/store/runs'

const current = 'current'

export const noTree =
  'This server did not record a tree for this snapshot, so what changed then cannot be shown.'

export function hasTree(snapshot: DiffSnapshot): boolean {
  return Boolean(snapshot.tree && snapshot.parentTree)
}

export function SummaryStrip({
  run,
  files,
  snapshots,
  selected,
  onSelect,
  base,
  wrap,
  onWrap,
  loading,
  onRefresh,
  onJump,
  onCollapseAll,
  publishable,
}: {
  run: RunRecord
  files: PatchFile[]
  snapshots: DiffSnapshot[]
  selected: DiffSnapshot | null
  onSelect: (time: string | null) => void
  base: string
  wrap: boolean
  onWrap: (wrap: boolean) => void
  loading: boolean
  onRefresh: () => void
  onJump: (path: string) => void
  onCollapseAll: (collapsed: boolean) => void
  publishable: boolean
}) {
  const caps = useCapability()
  const local = useCanReviewLocally(run)
  const [reviewing, setReviewing] = useState(false)
  const more = useRef<HTMLButtonElement>(null)
  const additions = files.reduce((sum, file) => sum + file.additions, 0)
  const deletions = files.reduce((sum, file) => sum + file.deletions, 0)
  const counts = (
    <>
      <span>
        {files.length} file{files.length === 1 ? '' : 's'}
      </span>
      <span className="@max-[640px]:sr-only">
        <Counts additions={additions} deletions={deletions} />
      </span>
    </>
  )

  return (
    <div
      role="group"
      aria-label="Changes"
      className="flex min-h-8 min-w-0 shrink-0 flex-wrap items-center gap-x-1 gap-y-0.5 border-b border-seam bg-chrome px-2 py-0.5 text-ui-sm text-muted"
    >
      <span className="hidden items-center gap-2 px-1 tabular-nums @min-[780px]:flex">{counts}</span>
      <Menu>
        <MenuTrigger asChild>
          <Button variant="ghost" size="sm" disabled={files.length === 0} className="tabular-nums @min-[780px]:hidden">
            {counts}
            <ChevronDown />
          </Button>
        </MenuTrigger>
        <MenuContent align="start" className="max-w-[min(32rem,calc(100vw-1rem))]">
          {files.map((file) => (
            <MenuItem key={file.path} onSelect={() => onJump(file.path)}>
              <span className="min-w-0 flex-1 truncate font-code">{file.path}</span>
              <Counts additions={file.additions} deletions={file.deletions} />
            </MenuItem>
          ))}
        </MenuContent>
      </Menu>
      <IntervalMenu snapshots={snapshots} selected={selected} onSelect={onSelect} base={base} />
      <ConflictChips run={run} />
      <span className="ml-auto flex items-center gap-1">
        <Button
          variant={wrap ? 'secondary' : 'ghost'}
          size="icon-sm"
          label="Wrap lines"
          aria-pressed={wrap}
          onClick={() => onWrap(!wrap)}
        >
          <WrapText />
        </Button>
        <Button variant="ghost" size="icon-sm" label="Refresh" onClick={onRefresh}>
          <RefreshCw className={cn(loading && 'animate-spin motion-reduce:animate-none')} />
        </Button>
        <Menu>
          <MenuTrigger asChild>
            <Button ref={more} variant="ghost" size="icon-sm" label="More">
              <Ellipsis />
            </Button>
          </MenuTrigger>
          <MenuContent align="end">
            <MenuItem disabled={files.length === 0} onSelect={() => onCollapseAll(false)}>
              Expand all files
            </MenuItem>
            <MenuItem disabled={files.length === 0} onSelect={() => onCollapseAll(true)}>
              Collapse all files
            </MenuItem>
            {local && (
              <>
                <MenuSeparator />
                <MenuItem onSelect={() => setReviewing(true)}>Review locally…</MenuItem>
              </>
            )}
          </MenuContent>
        </Menu>
        {publishable && caps.hasMethod('run.git.status') && <PublishDialog key={run.id} run={run} />}
      </span>
      {local && <ReviewLocallyDialog run={run} open={reviewing} onOpenChange={setReviewing} returnFocus={more} />}
    </div>
  )
}

function IntervalMenu({
  snapshots,
  selected,
  onSelect,
  base,
}: {
  snapshots: DiffSnapshot[]
  selected: DiffSnapshot | null
  onSelect: (time: string | null) => void
  base: string
}) {
  return (
    <Menu>
      <MenuTrigger asChild>
        <Button variant="ghost" size="sm">
          <History />
          <span className="@max-[640px]:sr-only">
            {selected ? (
              <>
                What changed <RelativeTime at={selected.time} />
              </>
            ) : (
              'Current diff'
            )}
          </span>
          <ChevronDown />
        </Button>
      </MenuTrigger>
      <MenuContent align="start" className="w-72">
        <MenuRadioGroup
          value={selected?.time ?? current}
          onValueChange={(value) => onSelect(value === current ? null : value)}
        >
          <MenuRadioItem value={current} description={`Against ${base.slice(0, 8) || 'the fork point'}`}>
            Current diff
          </MenuRadioItem>
          <MenuSeparator />
          <MenuLabel>
            {snapshots.length === 0 ? 'No intervals since you opened the dashboard.' : 'Change intervals'}
          </MenuLabel>
          {snapshots.map((snapshot, i) => {
            const additions = snapshot.files.reduce((sum, file) => sum + file.additions, 0)
            const deletions = snapshot.files.reduce((sum, file) => sum + file.deletions, 0)
            return (
              <MenuRadioItem
                key={snapshot.time + i}
                value={snapshot.time}
                disabled={!hasTree(snapshot)}
                description={
                  hasTree(snapshot) ? (
                    <>
                      {snapshot.truncated ? (
                        `${snapshot.files.length}+ files`
                      ) : (
                        <>
                          {snapshot.files.length} file{snapshot.files.length === 1 ? '' : 's'} ·{' '}
                          <Counts additions={additions} deletions={deletions} />
                        </>
                      )}
                      {snapshot.historyGap && ' · History gap'}
                    </>
                  ) : (
                    snapshot.snapshotError ?? (snapshot.historyGap ? 'Snapshot history is unavailable for this interval.' : noTree)
                  )
                }
              >
                What changed <RelativeTime at={snapshot.time} />
              </MenuRadioItem>
            )
          })}
        </MenuRadioGroup>
      </MenuContent>
    </Menu>
  )
}
