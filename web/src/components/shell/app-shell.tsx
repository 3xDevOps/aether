import { PaletteDialogs } from '@/components/palette/dialogs'
import { CenterView } from '@/components/shell/center-view'
import { Sidebar } from '@/components/shell/sidebar'
import { StatusBar } from '@/components/shell/status-bar'

export function AppShell() {
  return (
    <div className="flex h-full flex-col">
      <div className="flex min-h-0 flex-1">
        <Sidebar />
        <main className="min-w-0 flex-1 overflow-hidden">
          <CenterView />
        </main>
      </div>
      <StatusBar />
      {/* The launch and inject forms, hosted by the shell so every surface
          that opens one reaches the same host. */}
      <PaletteDialogs />
    </div>
  )
}
