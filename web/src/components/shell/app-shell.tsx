import { AssistantPanel } from '@/components/assistant/panel'
import { CommandPalette } from '@/components/palette'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { CenterView, focusView } from '@/components/shell/center-view'
import { ConnectionAnnouncer } from '@/components/shell/connection'
import { useNavShortcuts } from '@/components/shell/nav-shortcuts'
import { Sidebar } from '@/components/shell/sidebar'
import { TopBar } from '@/components/shell/top-bar'
import { ShortcutsDialog } from '@/components/shortcuts'
import { UpdateCenter } from '@/components/update-banner'
import { ProfileDialog } from '@/routes/members/personal'
import { useTeamRefresh } from '@/routes/team'
import { useStore } from '@/store'

export function AppShell() {
  useNavShortcuts()
  useTeamRefresh()
  const profileOpen = useStore((s) => s.profileOpen)
  const setProfileOpen = useStore((s) => s.setProfileOpen)
  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas text-ui text-text">
      <a
        href="#main"
        onClick={(event) => {
          event.preventDefault()
          focusView()
        }}
        className="sr-only z-50 rounded-control bg-raised px-3 py-1.5 text-ui text-text focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
      </a>
      <TopBar />
      <div className="relative flex min-h-0 flex-1 overflow-hidden">
        <Sidebar />
        <main
          id="main"
          tabIndex={-1}
          className="min-h-0 min-w-0 flex-1 overflow-hidden bg-canvas pr-[env(safe-area-inset-right)] pb-[env(safe-area-inset-bottom)] outline-none"
        >
          <CenterView />
        </main>
        <AssistantPanel />
      </div>
      <CommandPalette />
      <PaletteDialogs />
      <ShortcutsDialog />
      <ProfileDialog open={profileOpen} onOpenChange={setProfileOpen} />
      <UpdateCenter />
      <ConnectionAnnouncer />
    </div>
  )
}
