import { Chip } from '@/components/ui/heroui'
import { RunList } from '@/components/run-list'
import { ViewHeader } from '@/components/view-header'
import { registerRoute } from '@/routes/registry'
import { useStore } from '@/store'
import { useListedRuns } from '@/store/hooks'

// One flat list in group order, next to the board's three columns. The
// command palette is what points at it.
function Overview() {
  const runs = useListedRuns(useStore((s) => s.activeWorkspace))
  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader
        title="All runs"
        titleAdornment={
          <Chip color="default" variant="soft" size="sm">
            <Chip.Label>{runs.length} total</Chip.Label>
          </Chip>
        }
        subtitle="Current workspace · Needs you first"
      />
      <main className="min-h-0 flex-1 overflow-y-auto">
        <RunList runs={runs} empty="No runs yet." />
      </main>
    </div>
  )
}

registerRoute('overview', Overview)
