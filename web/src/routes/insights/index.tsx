import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ViewHeader } from '@/components/view-header'
import { api, ApiError, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { message } from '@/lib/format'
import { useConnectionProblem } from '@/components/shell/connection'
import type { InsightsParams, InsightsResult } from '@/lib/types'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { Heatmap } from '@/routes/insights/heatmap'
import { plural, rfc3339, weekRange, type SectionLoad } from '@/routes/insights/model'
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
function useScope(client: Api, scope: InsightsParams['scope'], workspace: string, timeZone: string, connection: string) {
  const caps = useCapability()
  const [load, setLoad] = useState<SectionLoad>({ kind: 'loading' })
  const [prErrors, setPRErrors] = useState<Record<string, string>>({})
  const [rereadError, setRereadError] = useState('')
  const requestID = useRef(0)

  // A finished run's pull request stays open in the record until run.pr.status reads it again from GitHub.
  const refreshPRs = useCallback(async (id: number, result: InsightsResult, params: InsightsParams) => {
    let changed = false
    for (const run of result.recent.filter((r) => r.pr_state === 'open').slice(0, 10)) {
      if (id !== requestID.current) return
      try {
        const status = await client.runPRStatus({ run_id: run.run_id })
        if (status.error) setPRErrors((errors) => ({ ...errors, [run.run_id]: status.error! }))
        if (status.pull_request && status.pull_request.state !== run.pr_state) changed = true
      } catch (error) {
        setPRErrors((errors) => ({ ...errors, [run.run_id]: message(error) }))
      }
    }
    if (!changed || id !== requestID.current) return
    try {
      const fresh = await client.insightsGet(params)
      if (id === requestID.current) setLoad({ kind: 'ready', result: fresh })
    } catch (error) {
      if (id === requestID.current) setRereadError(message(error))
    }
  }, [client])

  const read = useCallback(async () => {
    const id = ++requestID.current
    const { from, to } = weekRange(new Date())
    setLoad({ kind: 'loading' })
    setPRErrors({})
    setRereadError('')
    const params = { workspace_id: workspace, scope, from: rfc3339(from), to: rfc3339(to), time_zone: timeZone }
    try {
      const result = await client.insightsGet(params)
      if (id !== requestID.current) return
      setLoad({ kind: 'ready', result })
      if (scope === 'mine' && caps.hasMethod('run.pr.status')) void refreshPRs(id, result, params)
    } catch (error) {
      if (id !== requestID.current) return
      setLoad(error instanceof ApiError && (error.status === 404 || error.code === METHOD_NOT_FOUND) ? { kind: 'unsupported' } : { kind: 'failed', message: message(error) })
    }
  }, [client, workspace, scope, timeZone, caps, refreshPRs])

  useEffect(() => {
    if (connection === 'live') void read()
  }, [connection, read])

  return { load, read, prErrors, rereadError }
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
  const mine = useScope(client, 'mine', workspace, timeZone, connection)
  const team = useScope(client, 'everyone', workspace, timeZone, connection)
  const waiting = mine.load.kind === 'loading' && connection !== 'live'
  const controls = <WorkspaceSelect workspace={workspace} onWorkspace={setWorkspace} />

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Insights" actions={mobile ? undefined : controls} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-4xl min-w-0 flex-col gap-8 px-4 py-6 sm:px-6">
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
              prErrors={mine.prErrors}
            />
          )}
        </div>
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
