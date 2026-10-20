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
import { api } from '@/lib/api'
import { clockTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { ConflictChips } from '@/routes/diff/conflict-chips'
import type { PatchFile } from '@/routes/diff/parse'
import { Counts } from '@/routes/diff/patch-view'
import { PublishDialog } from '@/routes/diff/publish-dialog'
import { useReviewing } from '@/routes/diff/review'
import { ReviewLocallyDialog, useCanReviewLocally } from '@/routes/diff/review-commands'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { initialDiff, readCheckpoints, type Checkpoint, type Comparison } from '@/store/diff'
import type { RunRecord } from '@/store/runs'

const menuFiles = 100

export const noTree =
  'This server did not record a tree for this snapshot, so changes since then cannot be shown.'

/** Why a checkpoint cannot be compared against, or undefined when it can. A
 * history gap before a recorded tree does not stop a diff since that tree. */
function unavailable(checkpoint: Checkpoint): string | undefined {
  return checkpoint.tree ? undefined : (checkpoint.snapshotError ?? noTree)
}

export function SummaryStrip({
  run,
  files,
  comparison,
  onCompare,
  wrap,
  onWrap,
  loading,
  behind,
  onRefresh,
  onJump,
  onCollapseAll,
  publishable,
}: {
  run: RunRecord
  files: PatchFile[]
  comparison: Comparison
  onCompare: (comparison: Comparison) => void
  wrap: boolean
  onWrap: (wrap: boolean) => void
  loading: boolean
  /** The stored patch is large and a newer snapshot has arrived that it does not show. */
  behind: boolean
  onRefresh: () => void
  onJump: (path: string) => void
  onCollapseAll: (collapsed: boolean) => void
  publishable: boolean
}) {
  const caps = useCapability()
  const local = useCanReviewLocally(run)
  const commenting = useReviewing(run.id)
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
          {files.slice(0, menuFiles).map((file) => (
            <MenuItem key={file.path} onSelect={() => onJump(file.path)}>
              <span className="min-w-0 flex-1 truncate font-code">{file.path}</span>
              <Counts additions={file.additions} deletions={file.deletions} />
            </MenuItem>
          ))}
          {files.length > menuFiles && (
            <MenuLabel>{(files.length - menuFiles).toLocaleString('en-US')} more files are not listed.</MenuLabel>
          )}
        </MenuContent>
      </Menu>
      <CompareMenu run={run} comparison={comparison} onSelect={onCompare} />
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
        {behind ? (
          <Button variant="secondary" size="sm" onClick={onRefresh}>
            <RefreshCw className={cn(loading && 'animate-spin motion-reduce:animate-none')} />
            New changes · Refresh
          </Button>
        ) : (
          <Button variant="ghost" size="icon-sm" label="Refresh" onClick={onRefresh}>
            <RefreshCw className={cn(loading && 'animate-spin motion-reduce:animate-none')} />
          </Button>
        )}
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
        {publishable && caps.hasMethod('run.git.status') && <PublishDialog key={run.id} run={run} quiet={commenting || caps.hasMethod('run.git.log')} />}
      </span>
      {local && <ReviewLocallyDialog run={run} open={reviewing} onOpenChange={setReviewing} returnFocus={more} />}
    </div>
  )
}

const shortSha = (sha: string) => sha.slice(0, 7)

function CompareMenu({ run, comparison, onSelect }: { run: RunRecord; comparison: Comparison; onSelect: (comparison: Comparison) => void }) {
  const diff = useStore((s) => s.diffs[run.id] ?? initialDiff)
  const { checkpoints, history, historyError } = diff
  // A recorded patch names its base by tree, a live one by commit.
  const startable = Boolean(diff.forkTree && diff.base && run.base_commit && diff.base !== run.base_commit && diff.base !== diff.forkTree)
  const key = (checkpoint: Checkpoint) => `since:${checkpoint.tree ?? checkpoint.time}`
  const selected =
    comparison.kind === 'since' ? key(comparison.checkpoint) : comparison.kind
  return (
    <Menu>
      <MenuTrigger asChild>
        <Button variant="ghost" size="sm">
          <History />
          <span className="@max-[640px]:sr-only">
            {comparison.kind === 'all' && 'All changes'}
            {comparison.kind === 'start' && 'Since the run started'}
            {comparison.kind === 'since' && `Since ${clockTime(comparison.checkpoint.time)}`}
          </span>
          <ChevronDown />
        </Button>
      </MenuTrigger>
      <MenuContent align="start" className="w-80">
        <MenuRadioGroup
          value={selected}
          onValueChange={(value) => {
            if (value === 'all' || value === 'start') return onSelect({ kind: value })
            const checkpoint = checkpoints.find((entry) => key(entry) === value)
            if (checkpoint) onSelect({ kind: 'since', checkpoint })
          }}
        >
          <MenuRadioItem
            value="all"
            description={`Against ${run.base_branch || 'the base branch'}${diff.base ? ` @ ${shortSha(diff.base)}` : ''}`}
          >
            All changes
          </MenuRadioItem>
          {startable && (
            <MenuRadioItem
              value="start"
              description={`Against ${shortSha(run.base_commit!)}, where the run forked. Includes upstream work merged since.`}
            >
              Since the run started
            </MenuRadioItem>
          )}
          <MenuSeparator />
          <MenuLabel>
            {history === undefined && !historyError
              ? 'Loading checkpoints…'
              : checkpoints.length === 0 && !historyError
                ? 'No checkpoints recorded yet.'
                : 'Changes since a checkpoint'}
          </MenuLabel>
          {checkpoints.map((checkpoint, i) => {
            const blocked = unavailable(checkpoint)
            const older = checkpoints[i + 1]
            const committed =
              checkpoint.headSubject && older?.head && checkpoint.head && checkpoint.head !== older.head
            return (
              <MenuRadioItem
                key={key(checkpoint) + i}
                value={key(checkpoint)}
                disabled={blocked !== undefined}
                description={
                  blocked ??
                  (committed ? (
                    checkpoint.headSubject
                  ) : (
                    <>
                      {checkpoint.files} file{checkpoint.files === 1 ? '' : 's'}
                      {!checkpoint.truncated && (
                        <>
                          {' · '}
                          <Counts additions={checkpoint.additions} deletions={checkpoint.deletions} />
                        </>
                      )}
                    </>
                  ))
                }
              >
                {clockTime(checkpoint.time)} · <RelativeTime at={checkpoint.time} />
              </MenuRadioItem>
            )
          })}
        </MenuRadioGroup>
        {historyError && (
          <>
            <MenuLabel>Checkpoints could not be loaded: {historyError}</MenuLabel>
            <MenuItem
              onSelect={(event) => {
                event.preventDefault()
                void readCheckpoints(useStore, api, run, history !== undefined)
              }}
            >
              Retry
            </MenuItem>
          </>
        )}
        {history?.more && (
          <MenuItem
            onSelect={(event) => {
              event.preventDefault()
              void readCheckpoints(useStore, api, run, true)
            }}
          >
            Show older checkpoints
          </MenuItem>
        )}
      </MenuContent>
    </Menu>
  )
}
