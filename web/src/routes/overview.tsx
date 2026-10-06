import { Badge } from '@/components/ui/badge'
import { RunList } from '@/components/run-list'
import { ViewHeader } from '@/components/view-header'
import { registerRoute } from '@/routes/registry'
import { useListedRuns } from '@/store/hooks'

function Overview() {
  const runs = useListedRuns('')
  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader
        title="All workspaces"
        titleAdornment={<Badge>{runs.length} runs</Badge>}
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <RunList runs={runs} empty="No runs yet." />
      </div>
    </div>
  )
}

registerRoute('overview', Overview)
