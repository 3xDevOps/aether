import { useEffect, useState } from 'react'
import { useAgentList } from '@/routes/agents/use-agents'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import '@/routes/missions/conflicts'
import { SwarmDetail } from '@/routes/missions/detail'
import { SwarmList } from '@/routes/missions/list'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'

const pageSize = 50

export function MissionRoute({ params, client = api }: RouteProps & { client?: Api }) {
  const missionID = params.missionId
  const workspaceID = useStore((s) => s.activeWorkspace)
  const detail = useStore((s) => (missionID ? s.missionDetails[missionID] : undefined))
  const loading = useStore((s) => s.missionLoading)
  const error = useStore((s) => s.missionError)
  const nextCursor = useStore((s) => (s.missionListWorkspace === s.activeWorkspace ? s.missionNextCursor : null))
  const cap = useCapability()
  const self = useSelf()
  const { agents } = useAgentList(client)
  const [refresh, setRefresh] = useState(0)
  const [loadingMore, setLoadingMore] = useState(false)

  useEffect(() => {
    let live = true
    const { setMissions, setMissionDetail, setMissionLoading, setMissionError } = useStore.getState()
    setMissionLoading(true)
    const load = async () => {
      try {
        if (missionID) {
          const result = await client.missionShow(missionID)
          if (!live) return
          setMissionDetail({
            mission: result.mission,
            tasks: result.tasks,
            attempts: result.attempts ?? [],
            submissions: result.submissions ?? [],
            diagnostics: result.diagnostics ?? [],
            questions: result.questions ?? [],
          })
        } else {
          const result = await client.missionList({ workspace_id: workspaceID, limit: pageSize })
          if (!live) return
          setMissions(workspaceID, result.missions, result.next_cursor)
        }
        setMissionLoading(false)
      } catch (err) {
        if (live) setMissionError(message(err))
      }
    }
    if (workspaceID || missionID) void load()
    else setMissionLoading(false)
    return () => {
      live = false
    }
  }, [client, missionID, refresh, workspaceID])

  const loadMore = async () => {
    if (!workspaceID || !nextCursor || loadingMore) return
    setLoadingMore(true)
    try {
      const result = await client.missionList({ workspace_id: workspaceID, limit: pageSize, before: nextCursor })
      useStore.getState().setMissions(workspaceID, result.missions, result.next_cursor, true)
    } catch (err) {
      useStore.getState().setMissionError(message(err))
    } finally {
      setLoadingMore(false)
    }
  }

  if (missionID) {
    return (
      <SwarmDetail
        missionID={missionID}
        detail={detail}
        agents={agents}
        error={error}
        loading={loading}
        client={client}
        onChanged={() => setRefresh((value) => value + 1)}
      />
    )
  }
  return (
    <SwarmList
      agents={agents}
      canLaunch={cap.hasMethod('mission.create') && allowed('launch', self)}
      error={error}
      loading={loading}
      hasMore={Boolean(nextCursor)}
      loadingMore={loadingMore}
      onLoadMore={() => void loadMore()}
    />
  )
}

registerRoute('missions', MissionRoute)
