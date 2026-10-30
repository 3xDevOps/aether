import { memo, useCallback, useEffect, useMemo, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { Eye } from '@/components/icons'
import { EmptyState } from '@/components/ui/empty-state'
import { FailureCallout } from '@/components/ui/failure-callout'
import { InsetList, InsetListItem } from '@/components/ui/inset-list'
import { RadioPills, RadioPillsItem } from '@/components/ui/radio-pills'
import { RelativeTime } from '@/components/ui/relative-time'
import { Skeleton } from '@/components/ui/skeleton'
import { StateLine } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import type { Watch } from '@/lib/types'
import { conditionLabel, isLive, sourceLabel, stateWords, targetKindLabel, watchRoute } from '@/lib/watches'
import { WatchDetail } from '@/routes/settings/watch-detail'
import { useStore } from '@/store'
import { loadWatches } from '@/store/watches'

type Filter = 'active' | 'all'

const order: Record<Watch['state'], number> = { error: 0, active: 1, paused: 2, done: 3, expired: 4 }

function sorted(watches: Watch[]): Watch[] {
  return [...watches].sort((a, b) => order[a.state] - order[b.state] || b.updated_at.localeCompare(a.updated_at))
}

export function conditionsText(watch: Pick<Watch, 'target' | 'conditions'>): string {
  return watch.conditions.map((condition) => conditionLabel(watch.target.kind, condition)).join(', ')
}

const WatchRow = memo(function WatchRow({ watch, onOpen }: { watch: Watch; onOpen: (watch: Watch) => void }) {
  const state = stateWords[watch.state]
  const notice = watch.last_notification
  return (
    <InsetListItem onOpen={() => onOpen(watch)} className="items-start">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex min-w-0 items-baseline gap-2">
          <span className="min-w-0 truncate text-ui font-medium text-text">{watch.label || watch.target_title}</span>
          <span className="shrink-0 text-ui-sm text-muted">{[targetKindLabel(watch.target), sourceLabel(watch)].filter(Boolean).join(' · ')}</span>
        </span>
        <StateLine tone={state.tone} label={state.label} strong={watch.state === 'error'}>
          {state.label} · {conditionsText(watch)}
        </StateLine>
        {watch.state === 'error' && watch.error && <span className="break-words text-ui-sm text-state-failed">{watch.error}</span>}
        <span className="flex min-w-0 flex-wrap gap-x-1.5 text-ui-sm text-muted">
          {notice
            ? <span className="min-w-0 truncate">Last notice: {notice.title} · <RelativeTime at={notice.created_at} /></span>
            : <span>No notices yet</span>}
          {watch.last_check_at && <span>· Checked <RelativeTime at={watch.last_check_at} /></span>}
          {watch.next_check_at && isLive(watch) && <span>· Next check <RelativeTime at={watch.next_check_at} /></span>}
        </span>
      </div>
    </InsetListItem>
  )
})

/** Settings › Watches: the viewer's watches, and the detail of the one in the address. */
export function WatchesSection({ client }: { client: Api }) {
  const watches = useStore(useShallow((s) => Object.values(s.watches)))
  const open = useStore((s) => (s.route.name === 'settings' ? s.route.params.watch : undefined))
  const navigate = useStore((s) => s.navigate)
  const [filter, setFilter] = useState<Filter>('active')
  const [load, setLoad] = useState<{ kind: 'loading' } | { kind: 'ready' } | { kind: 'failed'; error: string }>({ kind: 'loading' })
  const [attempt, setAttempt] = useState(0)
  const skeleton = useDelayed(load.kind === 'loading' && watches.length === 0)

  useEffect(() => {
    let live = true
    setLoad({ kind: 'loading' })
    loadWatches(useStore, client).then(
      () => live && setLoad({ kind: 'ready' }),
      (err: unknown) => live && setLoad({ kind: 'failed', error: errorSentence(err) }),
    )
    return () => {
      live = false
    }
  }, [client, attempt])

  const shown = useMemo(() => sorted(filter === 'active' ? watches.filter(isLive) : watches), [watches, filter])
  const openWatch = useCallback((watch: Watch) => {
    const route = watchRoute(watch.id)
    navigate(route.name, route.params)
  }, [navigate])
  const hidden = watches.length - shown.length

  return (
    <section id="watch-list" aria-label="Watches" className="flex min-w-0 scroll-mt-4 flex-col gap-3">
      <RadioPills aria-label="Show" value={filter} onValueChange={(value) => setFilter(value as Filter)}>
        <RadioPillsItem value="active">Active</RadioPillsItem>
        <RadioPillsItem value="all">All</RadioPillsItem>
      </RadioPills>
      {load.kind === 'failed' && <FailureCallout error={load.error} onRetry={() => setAttempt((n) => n + 1)} />}
      {skeleton && (
        <div aria-hidden className="flex flex-col gap-1.5">
          {[0, 1, 2].map((i) => <Skeleton key={i} className="h-14" />)}
        </div>
      )}
      {shown.length > 0 && (
        <InsetList aria-label={filter === 'active' ? 'Active watches' : 'All watches'}>
          {shown.map((watch) => <WatchRow key={watch.id} watch={watch} onOpen={openWatch} />)}
        </InsetList>
      )}
      {load.kind === 'ready' && shown.length === 0 && (
        hidden > 0
          ? <p className="text-ui text-muted">No active watches. {hidden === 1 ? 'One watch has' : `${hidden} watches have`} ended; show All to see {hidden === 1 ? 'it' : 'them'}.</p>
          : (
            <EmptyState icon={<Eye />} title="No watches yet" className="py-10">
              A watch has the server keep checking a run or pull request after you close the dashboard, and notify you here and on your push devices when it needs you, finishes, or its checks or reviews come in. Choose Watch… in a run&apos;s menu or beside a run&apos;s pull request.
            </EmptyState>
          )
      )}
      {open && <WatchDetail key={open} watchID={open} client={client} onClose={() => navigate('settings', { section: 'watches' })} />}
    </section>
  )
}
