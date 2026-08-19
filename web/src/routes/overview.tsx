import { RunList } from '@/components/run-list'
import { ViewHeader } from '@/components/view-header'
import { registerRoute } from '@/routes/registry'
import { useAttentionRuns } from '@/store/hooks'

// One flat attention-ordered list, next to the board's four buckets. The
// command palette is what points at it.
function Overview() {
  const runs = useAttentionRuns()
  return (
    <div className="flex h-full flex-col">
      <ViewHeader title="All runs" subtitle={`${runs.length} total`} />
      <div className="flex-1 overflow-y-auto">
        <RunList runs={runs} empty="No runs yet." />
      </div>
    </div>
  )
}

registerRoute('overview', Overview)
