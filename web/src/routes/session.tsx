import { RunList } from '@/components/run-list'
import { ViewHeader } from '@/components/view-header'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useAttentionRuns } from '@/store/hooks'

function SessionView({ params }: RouteProps) {
  const session = useStore((s) => s.sessions[params.sessionId])
  const runs = useAttentionRuns().filter(
    (entry) => entry.run.session_id === params.sessionId,
  )

  if (!session) {
    return <p className="p-4 text-sm text-muted-foreground">Unknown session.</p>
  }
  return (
    <div className="flex h-full flex-col">
      <ViewHeader title={session.name} subtitle={session.base_branch} />
      <div className="flex-1 overflow-y-auto">
        <RunList runs={runs} empty="No runs in this session yet." />
      </div>
    </div>
  )
}

registerRoute('session', SessionView)
