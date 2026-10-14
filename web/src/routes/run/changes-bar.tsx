import { useEffect, useId } from 'react'
import { Button } from '@/components/ui/button'
import { WorkGroup } from '@/components/ui/timeline'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'
import { ChangedFileRow } from '@/routes/diff/file-list'
import { Counts } from '@/routes/diff/patch-view'
import { isLiveRun } from '@/routes/files/sources'
import { sessionColumn } from '@/routes/run/session-rows'
import { useStore } from '@/store'
import { diffTotals, readPersistedDiff } from '@/store/diff'
import type { RunRecord } from '@/store/runs'

const expandKey = 'changes-bar'

export function ChangesBar({ run }: { run: RunRecord }) {
  const latest = useStore((s) => s.diffs[run.id]?.snapshots[0])
  const persisted = useStore((s) => s.diffs[run.id]?.persisted)
  const error = useStore((s) => s.diffs[run.id]?.persistedError)
  const connected = useStore((s) => s.connection === 'live')
  const expanded = useStore((s) => s.expandedRows[run.id]?.[expandKey] === true)
  const listID = useId()
  const checkout = isLiveRun(run)
  useEffect(() => {
    if (checkout && connected && !latest && persisted === undefined) void readPersistedDiff(useStore, api, run)
  }, [checkout, connected, latest, persisted, run])

  if (!checkout) return null
  const snapshot = latest ?? persisted
  if (!snapshot && error) {
    return (
      <div role="alert" className={cn(sessionColumn, 'flex shrink-0 items-center gap-2 pb-1 text-ui-sm text-state-failed')}>
        <span className="min-w-0 flex-1 break-words">Changes could not be read: {error}</span>
        <Button variant="quiet" onClick={() => useStore.getState().setDiff(run.id, { persisted: undefined })}>Retry</Button>
      </div>
    )
  }
  if (!snapshot?.files.length) return null
  const { files, truncated } = snapshot
  const totals = diffTotals(files)!
  const open = (params: Record<string, string>) => useStore.getState().navigate('run', { runId: run.id, view: 'changes', ...params })
  return (
    <section
      aria-label="Changes"
      title="Files changed in this run's worktree since its fork point"
      data-slot="changes-bar"
      className={cn(sessionColumn, 'flex shrink-0 flex-col pb-1')}
    >
      <div className="flex min-w-0 items-center gap-3">
        <div className="min-w-0 flex-1">
          <WorkGroup
            summary={`${files.length} ${files.length === 1 ? 'file' : 'files'} changed`}
            trailing={truncated ? undefined : <Counts {...totals} />}
            expanded={expanded}
            controls={listID}
            onToggle={() => useStore.getState().toggleSessionRow(run.id, expandKey)}
          />
        </div>
        <Button variant="link" size="sm" onClick={() => open({})}>Review</Button>
      </div>
      {expanded && (
        <ul id={listID} className="max-h-48 overflow-y-auto max-md:max-h-[30dvh]">
          {files.map((file) => (
            <li key={file.path}>
              <ChangedFileRow file={file} counts={!truncated} onOpen={(path) => open({ file: path })} />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
