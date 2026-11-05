import { useEffect } from 'react'
import { ChartGantt, List } from '@/components/icons'
import { SidebarRuns } from '@/components/shell/sidebar-runs'
import { RadioPills, RadioPillsItem } from '@/components/ui/radio-pills'
import { ViewHeader } from '@/components/view-header'
import { useIsMobile } from '@/lib/breakpoints'
import { redirectRoute } from '@/lib/url-state'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { BoardLanes } from '@/routes/board/lanes/board-lanes'
import { byAttention, useAccountNeeds, useAttention } from '@/routes/needs-you/attention'
import { registerRoute } from '@/routes/registry'
import { useStore } from '@/store'

/** The phone's session list or lanes; a wider window has the same list in its sidebar. */
function Sessions() {
  const mobile = useIsMobile()
  const items = useAttention((s) => s.items)
  const { devices, merges } = useAccountNeeds()
  const lanes = useStore((s) => s.sessionsLanes)
  useEffect(() => {
    if (!mobile) redirectRoute(useStore, { name: 'board', params: {} })
  }, [mobile])
  if (!mobile) return null
  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Sessions" />
      {lanes ? <SessionLanes /> : (
        <div className="flex min-h-0 min-w-0 flex-1 flex-col pt-2">
          <SidebarRuns regroup={(groups) => byAttention(groups, items, devices.length + merges.length)} />
        </div>
      )}
    </div>
  )
}

/** The phone top bar's List and Lanes choice for this route. */
export function SessionsViewSwitch() {
  const lanes = useStore((s) => s.sessionsLanes)
  const setLanes = useStore((s) => s.setSessionsLanes)
  return (
    <RadioPills aria-label="Sessions view" value={lanes ? 'lanes' : 'list'} onValueChange={(value) => setLanes(value === 'lanes')}>
      <RadioPillsItem value="list"><List aria-hidden className="size-4" />List</RadioPillsItem>
      <RadioPillsItem value="lanes"><ChartGantt aria-hidden className="size-4" />Lanes</RadioPillsItem>
    </RadioPills>
  )
}

function SessionLanes() {
  const { agents } = useAgentList()
  return <BoardLanes agentNames={agentDisplayNames(agents)} />
}

registerRoute('sessions', Sessions)
