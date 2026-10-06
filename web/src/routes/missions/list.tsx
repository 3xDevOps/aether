import { useMemo, useState } from 'react'
import { Plus } from '@/components/icons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Card, CardTitle } from '@/components/ui/card'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { EmptyState } from '@/components/ui/empty-state'
import { RelativeTime } from '@/components/ui/relative-time'
import { Skeleton } from '@/components/ui/skeleton'
import { StateLine } from '@/components/ui/status-dot'
import { ViewHeader } from '@/components/view-header'
import { useIsMobile } from '@/lib/breakpoints'
import { useDelayed } from '@/lib/hooks'
import type { AgentInfo, Mission } from '@/lib/types'
import {
  integratorLabel,
  missionFinal,
  objectiveTitle,
  phaseTone,
  phaseWord,
  useSwarmLines,
  type SwarmLine,
} from '@/routes/missions/swarm'
import { useStore } from '@/store'

function planningWord(mission: Mission): string {
  if (mission.phase !== 'planning' || mission.open_questions === 0) return phaseWord[mission.phase]
  return `Planning · ${mission.open_questions} question${mission.open_questions === 1 ? '' : 's'} for you`
}

function SwarmCard({ mission, line, agents }: { mission: Mission; line?: SwarmLine; agents: AgentInfo[] | null }) {
  const navigate = useStore((s) => s.navigate)
  const final = missionFinal(mission)
  const failedLaunch = !final && mission.integrator_launch_error
  const needsYou = line?.needsYou ?? false
  const reason = failedLaunch
    ? `Integrator did not launch: ${mission.integrator_launch_error}`
    : (needsYou || line?.unread) && line?.reason ? line.reason : planningWord(mission)
  return (
    <Card data-mission-id={mission.id}>
      <StateLine
        tone={needsYou || failedLaunch ? 'needs-you' : phaseTone[mission.phase]}
        trailing={<RelativeTime at={mission.updated_at} />}
      >
        {reason}
      </StateLine>
      <CardTitle onOpen={() => navigate('missions', { missionId: mission.id })}>{objectiveTitle(mission.objective)}</CardTitle>
      <div className="flex min-h-4 min-w-0 items-center gap-1.5 text-ui-sm text-muted">
        <AgentGlyph agent={mission.integrator.harness} />
        <span className="min-w-0 truncate">{integratorLabel(mission, agents)}</span>
        <span aria-hidden>·</span>
        <span className="min-w-0 truncate">{line?.counts ?? 'No workers yet'}</span>
        {line && line.unread > 0 && <Badge tone="needs-you" className="ml-auto">{line.unread} unread</Badge>}
      </div>
    </Card>
  )
}

export function SwarmList({
  agents,
  canLaunch,
  error,
  loading,
  hasMore,
  loadingMore,
  onLoadMore,
}: {
  agents: AgentInfo[] | null
  canLaunch: boolean
  error: string | null
  loading: boolean
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
}) {
  const workspaceID = useStore((s) => s.activeWorkspace)
  const records = useStore((s) => s.missions)
  const mobile = useIsMobile()
  const lines = useSwarmLines()
  const [showFinished, setShowFinished] = useState(false)
  const { open, finished } = useMemo(() => {
    const missions = Object.values(records)
      .filter((mission) => !workspaceID || mission.workspace_id === workspaceID)
      .sort((a, b) => b.updated_at.localeCompare(a.updated_at))
    const waiting = (m: Mission) => Number(lines[m.id]?.needsYou ?? false)
    return {
      open: missions.filter((m) => !missionFinal(m)).sort((a, b) => waiting(b) - waiting(a)),
      finished: missions.filter(missionFinal),
    }
  }, [records, workspaceID, lines])
  const skeleton = useDelayed(loading && open.length + finished.length === 0)
  const newSwarm = canLaunch && (
    <Button size="sm" variant={mobile ? 'secondary' : 'primary'} onClick={() => useStore.getState().openPaletteDialog('launch')}>
      <Plus />
      New swarm
    </Button>
  )
  const empty = !loading && !error && open.length + finished.length === 0

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader title="Swarms" actions={(!mobile && newSwarm) || undefined} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-4xl min-w-0 flex-col gap-3 px-4 py-6 sm:px-6">
          {error && <Callout tone="failed" role="alert">{error}</Callout>}
          {skeleton && [0, 1, 2].map((key) => <div key={key} className="h-20"><Skeleton className="size-full" /></div>)}
          {empty && (
            <EmptyState title="No swarms yet" action={newSwarm}>
              A swarm hands one objective to an integrator agent, which splits it into tasks and runs a worker on each.
            </EmptyState>
          )}
          {open.length > 0 && (
            <ul aria-label="Swarms" className="grid gap-2 md:grid-cols-2">
              {open.map((mission) => (
                <li key={mission.id} className="min-w-0">
                  <SwarmCard mission={mission} line={lines[mission.id]} agents={agents} />
                </li>
              ))}
            </ul>
          )}
          {mobile && !empty && newSwarm && <div>{newSwarm}</div>}
          {finished.length > 0 && (
            <Collapsible open={showFinished || open.length === 0} onOpenChange={setShowFinished}>
              <CollapsibleTrigger>Finished ({finished.length})</CollapsibleTrigger>
              <CollapsibleContent>
                <ul aria-label="Finished swarms" className="mt-2 grid gap-2 md:grid-cols-2">
                  {finished.map((mission) => (
                    <li key={mission.id} className="min-w-0">
                      <SwarmCard mission={mission} line={lines[mission.id]} agents={agents} />
                    </li>
                  ))}
                </ul>
              </CollapsibleContent>
            </Collapsible>
          )}
          {hasMore && (
            <div className="flex justify-center">
              <Button size="sm" variant="secondary" onClick={onLoadMore} disabled={loadingMore}>
                {loadingMore ? 'Loading…' : 'Show more'}
              </Button>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
