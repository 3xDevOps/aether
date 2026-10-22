import { useCallback, useEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ViewHeader } from '@/components/view-header'
import { api, ApiError, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { message } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useConnectionProblem } from '@/components/shell/connection'
import type { InsightsParams, InsightsResult } from '@/lib/types'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { Heatmap } from '@/routes/insights/heatmap'
import { SystemTab } from '@/routes/insights/system'
import { hasOutcomes, plural, rfc3339, weekRange, type SectionLoad } from '@/routes/insights/model'
import { Breakdown, FinishedList, Section, StatsSection, Time, Usage } from '@/routes/insights/sections'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const METHOD_NOT_FOUND = -32601
const all = '@all'

function WorkspaceSelect({ workspace, onWorkspace }: { workspace: string; onWorkspace: (id: string) => void }) {
  const workspaces = useStore(useShallow((s) => Object.values(s.workspaces)))
  if (workspaces.length < 2) return null
  return (
    <Select value={workspace || all} onValueChange={(value) => onWorkspace(value === all ? '' : value)}>
      <SelectTrigger aria-label="Workspace" className="w-40 max-w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={all}>All workspaces</SelectItem>
        {workspaces.map((w) => <SelectItem key={w.id} value={w.id}>{w.name}</SelectItem>)}
      </SelectContent>
    </Select>
  )
}

/** Reads one scope of the last seven days. Answers to an older request are dropped. */
type PRChecks = { checked: Set<string>; setErrors: Dispatch<SetStateAction<Record<string, string>>> }

function useScope(client: Api, scope: InsightsParams['scope'], workspace: string, timeZone: string, connection: string, prs: PRChecks, onPRChange: () => void) {
  const caps = useCapability()
  const [load, setLoad] = useState<SectionLoad>({ kind: 'loading' })
  const [rereadError, setRereadError] = useState('')
  const requestID = useRef(0)
  const started = useRef(0)
  const landed = useRef(0)
  const lastParams = useRef<InsightsParams | null>(null)

  // Reads again without clearing what is shown; the team's success rate moves when a pull request merges.
  // An answer older than one already shown is dropped, so reads that overlap never put stale numbers back.
  const reload = useCallback(async (id: number, params: InsightsParams) => {
    const n = ++started.current
    try {
      const fresh = await client.insightsGet(params)
      if (id === requestID.current && n > landed.current && hasOutcomes(fresh)) {
        landed.current = n
        setLoad({ kind: 'ready', result: fresh })
      }
    } catch (error) {
      if (id === requestID.current && n > landed.current) setRereadError(message(error))
    }
  }, [client])

  const reread = useCallback(async () => {
    if (lastParams.current) await reload(requestID.current, lastParams.current)
  }, [reload])

  // A finished run's pull request stays open in the record until run.pr.status reads it again from GitHub.
  const refreshPRs = useCallback(async (id: number, result: InsightsResult, params: InsightsParams) => {
    let changed = false
    for (const run of result.recent.filter((r) => r.pr_state === 'open').slice(0, 10)) {
      if (id !== requestID.current) return
      if (prs.checked.has(run.run_id)) continue
      prs.checked.add(run.run_id)
      try {
        const status = await client.runPRStatus({ run_id: run.run_id })
        if (status.error) prs.setErrors((errors) => ({ ...errors, [run.run_id]: status.error! }))
        if (status.pull_request && status.pull_request.state !== run.pr_state) changed = true
      } catch (error) {
        prs.setErrors((errors) => ({ ...errors, [run.run_id]: message(error) }))
      }
    }
    if (!changed || id !== requestID.current) return
    onPRChange()
    await reload(id, params)
  }, [client, prs, onPRChange, reload])

  const read = useCallback(async () => {
    const id = ++requestID.current
    const { from, to } = weekRange(new Date())
    setLoad({ kind: 'loading' })
    setRereadError('')
    const params = { workspace_id: workspace, scope, from: rfc3339(from), to: rfc3339(to), time_zone: timeZone }
    lastParams.current = params
    const n = ++started.current
    try {
      const result = await client.insightsGet(params)
      if (id !== requestID.current) return
      if (!hasOutcomes(result)) {
        if (n > landed.current) setLoad({ kind: 'unsupported' })
        return
      }
      if (n > landed.current) {
        landed.current = n
        setLoad({ kind: 'ready', result })
      }
      if (caps.hasMethod('run.pr.status')) void refreshPRs(id, result, params)
    } catch (error) {
      if (id !== requestID.current || n < landed.current) return
      setLoad(error instanceof ApiError && (error.status === 404 || error.code === METHOD_NOT_FOUND) ? { kind: 'unsupported' } : { kind: 'failed', message: message(error) })
    }
  }, [client, workspace, scope, timeZone, caps, refreshPRs])

  useEffect(() => {
    if (connection === 'live') void read()
  }, [connection, read])

  return { load, read, reread, rereadError }
}

export function InsightsRoute({ client = api }: RouteProps & { client?: Api }) {
  const connection = useStore((s) => s.connection)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const workspaceNames = useStore(useShallow((s) => Object.fromEntries(Object.values(s.workspaces).map((w) => [w.id, w.name]))))
  const mobile = useIsMobile()
  const { agents } = useAgentList(client)
  const agentNames = useMemo(() => agentDisplayNames(agents), [agents])
  const [picked, setWorkspace] = useState<string | null>(null)
  const workspace = picked ?? activeWorkspace
  const resolvedZone = Intl.DateTimeFormat().resolvedOptions().timeZone
  const timeZone = resolvedZone && resolvedZone !== 'Etc/Unknown' ? resolvedZone : ''
  const problem = useConnectionProblem()
  const navigate = useStore((s) => s.navigate)
  const tab = useStore((s) => s.route.params.tab) === 'system' ? 'system' : 'activity'
  const [prErrors, setPRErrors] = useState<Record<string, string>>({})
  const checked = useRef(new Set<string>()).current
  const prs = useMemo(() => ({ checked, setErrors: setPRErrors }), [])
  useEffect(() => {
    checked.clear()
    setPRErrors({})
  }, [workspace, connection, checked])
  const mineReread = useRef<() => void>(() => {})
  const teamReread = useRef<() => void>(() => {})
  const rereadMine = useCallback(() => mineReread.current(), [])
  const rereadTeam = useCallback(() => teamReread.current(), [])
  const mine = useScope(client, 'mine', workspace, timeZone, connection, prs, rereadTeam)
  const team = useScope(client, 'everyone', workspace, timeZone, connection, prs, rereadMine)
  mineReread.current = () => void mine.reread()
  teamReread.current = () => void team.reread()
  const waiting = mine.load.kind === 'loading' && connection !== 'live'
  const controls = tab === 'activity' ? <WorkspaceSelect workspace={workspace} onWorkspace={setWorkspace} /> : null

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Insights" actions={mobile ? undefined : controls} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <Tabs value={tab} onValueChange={(value) => navigate('insights', value === 'system' ? { tab: 'system' } : {})}>
          <div className={cn('mx-auto flex w-full min-w-0 flex-col gap-6 px-4 py-6 sm:px-6', tab === 'system' ? 'max-w-6xl' : 'max-w-4xl')}>
            <TabsList aria-label="Insights view">
              <TabsTrigger value="activity">Activity</TabsTrigger>
              <TabsTrigger value="system">System</TabsTrigger>
            </TabsList>
            <TabsContent value="activity" className="flex min-w-0 flex-col gap-8">
              {mobile && controls && <div role="toolbar" aria-label="Insights actions" className="flex flex-wrap items-center gap-2">{controls}</div>}
              {waiting && (
                <Callout tone="neutral" role="status">{problem ?? 'Connecting'}. Insights load once the server connection is live.</Callout>
              )}
              {timeZone === '' && mine.load.kind === 'ready' && (
                <p className="text-ui-sm text-muted">This browser reports no time zone, so days are counted in UTC.</p>
              )}
              {mine.load.kind === 'ready' && mine.load.result.needs_you > 0 && (
                <p className="text-ui">
                  <Button variant="link" onClick={() => navigate('board')}>
                    {plural(mine.load.result.needs_you, 'run')} {mine.load.result.needs_you === 1 ? 'needs' : 'need'} you
                  </Button>
                </p>
              )}
              {mine.load.kind === 'ready' && mine.rereadError && (
                <p className="text-ui-sm text-muted">A pull request changed state, but reading Insights again failed: {mine.rereadError}</p>
              )}
              {mine.load.kind === 'unsupported' && (
                <Callout tone="neutral" role="status">This server does not report insights. Update the server to see them.</Callout>
              )}
              {mine.load.kind !== 'unsupported' && <>
              <StatsSection title="My statistics" load={waiting ? { kind: 'loading' } : mine.load} onRetry={() => void mine.read()} team={false} agentNames={agentNames} />
              <StatsSection title="Team statistics" load={waiting ? { kind: 'loading' } : team.load} onRetry={() => void team.read()} team agentNames={agentNames} />
              </>}
              {mine.load.kind === 'ready' && (
                <Details
                  result={mine.load.result}
                  workspaceOf={(id) => mine.load.kind === 'ready' ? mine.load.result.by_workspace.find((w) => w.workspace_id === id)?.name ?? workspaceNames[id] ?? id : id}
                  agentNames={agentNames}
                  prErrors={prErrors}
                />
              )}
            </TabsContent>
            <TabsContent value="system">
              <SystemTab client={client} />
            </TabsContent>
          </div>
        </Tabs>
      </div>
    </div>
  )
}

function Details({
  result,
  workspaceOf,
  agentNames,
  prErrors,
}: {
  result: InsightsResult
  workspaceOf: (id: string) => string
  agentNames: Record<string, string>
  prErrors: Record<string, string>
}) {
  return (
    <>
      <p className="text-ui-sm text-muted">The sections below cover only your runs in the last 7 days.</p>
      <FinishedList runs={result.recent} workspaceOf={workspaceOf} agentNames={agentNames} prErrors={prErrors} />
      {result.heatmap.some((d) => d.runs > 0) && (
        <Section title="Last 26 weeks">
          <Heatmap days={result.heatmap} />
        </Section>
      )}
      <Breakdown result={result} agentNames={agentNames} />
      <Time result={result} />
      <Usage result={result} />
    </>
  )
}

registerRoute('insights', InsightsRoute)
