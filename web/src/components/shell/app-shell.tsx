import { PaletteDialogs } from '@/components/palette/dialogs'
import { CenterView } from '@/components/shell/center-view'
import { useNavShortcuts } from '@/components/shell/nav-shortcuts'
import { Sidebar } from '@/components/shell/sidebar'
import { StatusBar } from '@/components/shell/status-bar'
import { UpdateBanners } from '@/components/update-banner'

export function AppShell() {
  useNavShortcuts()
  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      {/* The update surface stays above the workbench without stealing its
          vertical space when it has nothing to say. */}
      <div className="min-h-0 overflow-y-auto">
        <UpdateBanners />
      </div>
      <div className="relative flex min-h-0 flex-1">
        <Sidebar />
        <main className="min-w-0 flex-1 overflow-hidden bg-background">
          <CenterView />
        </main>
      </div>
      <StatusBar />
      {/* The launch, inject and forward forms, hosted by the shell so every
          surface that opens one reaches the same host. */}
      <PaletteDialogs />
    </div>
  )
}
