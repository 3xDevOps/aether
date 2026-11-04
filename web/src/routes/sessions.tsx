import { useEffect } from 'react'
import { SidebarRuns } from '@/components/shell/sidebar-runs'
import { ViewHeader } from '@/components/view-header'
import { useIsMobile } from '@/lib/breakpoints'
import { redirectRoute } from '@/lib/url-state'
import { byAttention, useAttention } from '@/routes/needs-you/attention'
import { registerRoute } from '@/routes/registry'
import { useStore } from '@/store'

/** The phone's session list; a wider window has the same list in its sidebar. */
function Sessions() {
  const mobile = useIsMobile()
  const items = useAttention((s) => s.items)
  useEffect(() => {
    if (!mobile) redirectRoute(useStore, { name: 'board', params: {} })
  }, [mobile])
  if (!mobile) return null
  return (
    <div className="flex h-full min-w-0 flex-col pt-2">
      <ViewHeader title="Sessions" />
      <SidebarRuns regroup={(groups) => byAttention(groups, items)} />
    </div>
  )
}

registerRoute('sessions', Sessions)
