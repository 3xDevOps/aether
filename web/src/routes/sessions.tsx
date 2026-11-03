import { SidebarRuns } from '@/components/shell/sidebar-runs'
import { ViewHeader } from '@/components/view-header'
import { registerRoute } from '@/routes/registry'

function Sessions() {
  return (
    <div className="flex h-full min-w-0 flex-col pt-2">
      <ViewHeader title="Sessions" />
      <SidebarRuns />
    </div>
  )
}

registerRoute('sessions', Sessions)
