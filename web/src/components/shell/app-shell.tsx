import { PaletteDialogs } from '@/components/palette/dialogs'
import { CenterView } from '@/components/shell/center-view'
import { Sidebar } from '@/components/shell/sidebar'
import { StatusBar } from '@/components/shell/status-bar'
import { UpdateBanners } from '@/components/update-banner'

export function AppShell() {
  return (
    <div className="flex h-full flex-col">
      {/* Above everything: an out-of-date binary is about the whole app,
          not about whichever view happens to be open. */}
      <UpdateBanners />
      <div className="flex min-h-0 flex-1">
        <Sidebar />
        <main className="min-w-0 flex-1 overflow-hidden">
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
