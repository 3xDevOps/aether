import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { EmptyState } from '@/components/ui/empty-state'
import { FailureCallout } from '@/components/ui/failure-callout'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { api, ApiError, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { canLaunch } from '@/lib/commands'
import { message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import type { InsightsParams, InsightsResult } from '@/lib/types'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { ActivityChart } from '@/routes/insights/activity-chart'
import { Heatmap } from '@/routes/insights/heatmap'
import { bucketSelection, finishedIn, headline, isEmpty, periodRange, periods, plural, rfc3339, type Period } from '@/routes/insights/model'
import { Breakdown, FinishedList, Section, Tiles, Time, Usage } from '@/routes/insights/sections'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability, useSelfRole } from '@/store/hooks'

const METHOD_NOT_FOUND = -32601
const all = '@all'

type Load =
  | { kind: 'loading' }
  | { kind: 'ready'; result: InsightsResult }
  | { kind: 'failed'; message: string }
  | { kind: 'unsupported' }

function unsupported(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 404 || error.code === METHOD_NOT_FOUND)
}

function Controls({
  period,
  onPeriod,
  workspace,
  onWorkspace,
  scope,
  onScope,
}: {
  period: Period
  onPeriod: (period: Period) => void
  workspace: string
  onWorkspace: (id: string) => void
  scope: InsightsParams['scope']
  onScope: (scope: InsightsParams['scope']) => void
}) {
  const workspaces = useStore(useShallow((s) => Object.values(s.workspaces)))
  return (
    <>
      <Select value={period} onValueChange={(value) => onPeriod(value as Period)}>
        <SelectTrigger aria-label="Period" className="w-36">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {periods.map((p) => <SelectItem key={p.value} value={p.value}>{p.label}</SelectItem>)}
        </SelectContent>
      </Select>
      {workspaces.length > 1 && (
        <Select value={workspace || all} onValueChange={(value) => onWorkspace(value === all ? '' : value)}>
          <SelectTrigger aria-label="Workspace" className="w-40 max-w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={all}>All workspaces</SelectItem>
            {workspaces.map((w) => <SelectItem key={w.id} value={w.id}>{w.name}</SelectItem>)}
          </SelectContent>
        </Select>
      )}
      <div role="group" aria-label="Whose runs" className="flex items-center gap-0.5">
        {(['mine', 'everyone'] as const).map((value) => (
          <Button key={value} size="sm" variant={scope === value ? 'secondary' : 'ghost'} aria-pressed={scope === value} onClick={() => onScope(value)}>
            {value === 'mine' ? 'Mine' : 'Everyone'}
          </Button>
        ))}
      </div>
    </>
  )
}

function Loading() {
  return (
    <div aria-hidden className="flex flex-col gap-4">
      <Skeleton className="h-5 w-2/3" />
      <div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
        {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-24" />)}
      </div>
      <Skeleton className="h-40" />
    </div>
  )
}

export function InsightsRoute({ client = api }: RouteProps & { client?: Api }) {
  const connection = useStore((s) => s.connection)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const workspaceNames = useStore(useShallow((s) => Object.fromEntries(Object.values(s.workspaces).map((w) => [w.id, w.name]))))
  const navigate = useStore((s) => s.navigate)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const caps = useCapability()
  const role = useSelfRole()
  const mobile = useIsMobile()
  const { agents } = useAgentList(client)
  const agentNames = useMemo(() => agentDisplayNames(agents), [agents])
  const [period, setPeriod] = useState<Period>('7d')
  const [workspace, setWorkspace] = useState(activeWorkspace)
  const [scope, setScope] = useState<InsightsParams['scope']>('mine')
  const [selected, setSelected] = useState<string | null>(null)
  const [load, setLoad] = useState<Load>({ kind: 'loading' })
  const requestID = useRef(0)
  const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone

  const read = useCallback(async () => {
    const id = ++requestID.current
    const { from, to } = periodRange(period, new Date())
    setLoad({ kind: 'loading' })
    setSelected(null)
    try {
      const result = await client.insightsGet({ workspace_id: workspace, scope, from: rfc3339(from), to: rfc3339(to), time_zone: timeZone })
      if (id === requestID.current) setLoad({ kind: 'ready', result })
    } catch (error) {
      if (id !== requestID.current) return
      setLoad(unsupported(error) ? { kind: 'unsupported' } : { kind: 'failed', message: message(error) })
    }
  }, [client, period, workspace, scope, timeZone])

  useEffect(() => {
    if (connection === 'live') void read()
  }, [connection, read])

  const skeleton = useDelayed(load.kind === 'loading')
  const periodInfo = periods.find((p) => p.value === period)!
  const controls = (
    <Controls period={period} onPeriod={setPeriod} workspace={workspace} onWorkspace={setWorkspace} scope={scope} onScope={setScope} />
  )

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Insights" actions={mobile ? undefined : controls} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-4xl min-w-0 flex-col gap-8 px-4 py-6 sm:px-6">
          {mobile && <div role="toolbar" aria-label="Insights actions" className="flex flex-wrap items-center gap-2">{controls}</div>}
          {load.kind === 'loading' && skeleton && <Loading />}
          {load.kind === 'unsupported' && (
            <Callout tone="neutral" role="status">This server does not report insights. Update the server to see them.</Callout>
          )}
          {load.kind === 'failed' && <FailureCallout error={load.message} onRetry={() => void read()} />}
          {load.kind === 'ready' && (
            <Insights
              result={load.result}
              periodLabel={periodInfo.label}
              before={periodInfo.previous}
              oneWorkspace={workspace !== ''}
              selected={selected}
              onSelect={setSelected}
              repositoryOf={(id) => load.result.by_workspace.find((w) => w.workspace_id === id)?.name ?? workspaceNames[id] ?? id}
              agentNames={agentNames}
              onBoard={() => navigate('board')}
              emptyAction={(
                <div className="flex flex-wrap justify-center gap-2">
                  {canLaunch({ cap: caps, role }) && <Button variant={mobile ? 'secondary' : 'primary'} onClick={() => openDialog('launch')}>New run</Button>}
                  {period !== '12m' && <Button variant="secondary" onClick={() => setPeriod('12m')}>Show the last 12 months</Button>}
                  {scope === 'mine' && <Button variant="secondary" onClick={() => setScope('everyone')}>Show everyone&apos;s runs</Button>}
                </div>
              )}
            />
          )}
        </div>
      </div>
    </div>
  )
}

function Insights({
  result,
  periodLabel,
  before,
  oneWorkspace,
  selected,
  onSelect,
  repositoryOf,
  agentNames,
  onBoard,
  emptyAction,
}: {
  result: InsightsResult
  periodLabel: string
  before: string
  oneWorkspace: boolean
  selected: string | null
  onSelect: (start: string | null) => void
  repositoryOf: (id: string) => string
  agentNames: Record<string, string>
  onBoard: () => void
  emptyAction: ReactNode
}) {
  const selection = selected ? bucketSelection(selected, result.bucket) : null
  const runs = selection ? result.recent.filter((run) => finishedIn(run, selection, result.time_zone)) : result.recent
  const needsYou = result.needs_you > 0 && (
    <Button variant="link" onClick={onBoard}>{plural(result.needs_you, 'run')} {result.needs_you === 1 ? 'needs' : 'need'} you</Button>
  )
  const heatmap = result.heatmap.some((d) => d.runs > 0) && (
    <Section title="Last 26 weeks">
      <Heatmap days={result.heatmap} />
    </Section>
  )

  if (isEmpty(result)) {
    return (
      <>
        {needsYou && <p className="text-ui">{needsYou}</p>}
        <EmptyState title="No runs in this period." action={emptyAction}>
          Runs you launch show up here once they start, with what they finished.
        </EmptyState>
        {heatmap}
      </>
    )
  }

  return (
    <>
      <p className="text-ui">
        {headline(result, periodLabel, oneWorkspace)}
        {needsYou && <> {needsYou}</>}
      </p>
      <Tiles result={result} before={before} />
      <Section title="Activity">
        <ActivityChart series={result.series} bucket={result.bucket} selected={selected} onSelect={onSelect} />
      </Section>
      <FinishedList
        runs={runs}
        capped={!selection && result.recent.length >= 50}
        filter={selection?.label ?? null}
        onClear={() => onSelect(null)}
        repositoryOf={repositoryOf}
        agentNames={agentNames}
      />
      {heatmap}
      <Breakdown result={result} agentNames={agentNames} />
      <Time result={result} />
      <Usage result={result} />
    </>
  )
}

registerRoute('insights', InsightsRoute)
