import { CommandPalette } from '@/components/palette'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { CenterView } from '@/components/shell/center-view'
import { useNavShortcuts } from '@/components/shell/nav-shortcuts'
import { Sidebar } from '@/components/shell/sidebar'
import { StatusBar } from '@/components/shell/status-bar'
import { UpdateBanners } from '@/components/update-banner'


export function AppShell() {
  useNavShortcuts()
  return (
    <div className="flex h-full min-h-0 flex-col bg-background text-[13px] leading-[1.4] text-foreground">
      {/* The update surface stays above the workbench without stealing its
          vertical space when it has nothing to say. */}
      <div className="min-h-0 min-w-0 shrink-0 max-h-[max(0px,calc(100dvh-35px-22px-10rem))] overflow-y-auto overscroll-contain">
        <UpdateBanners />
      </div>
      <div className="relative flex min-h-0 flex-1 overflow-hidden">
        <Sidebar />
        <main className="min-h-0 min-w-0 flex-1 overflow-hidden bg-background">
          <CenterView />
        </main>
      </div>
      <StatusBar />
      {/* Forms and the command center are global shell overlays. The command
          center itself is mounted once, independently of the status Slot. */}
      <CommandPalette />
      <PaletteDialogs />
    </div>
  )
}
