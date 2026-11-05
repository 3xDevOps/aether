import { AssistantPanel } from '@/components/assistant/panel'
import { useSharedPrompt } from '@/components/launch/shared-prompt'
import { CommandPalette } from '@/components/palette'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { CenterView, focusView } from '@/components/shell/center-view'
import { ConnectionAnnouncer } from '@/components/shell/connection'
import { useNavShortcuts } from '@/components/shell/nav-shortcuts'
import { PhoneTabBar } from '@/components/shell/phone-tab-bar'
import { Sidebar } from '@/components/shell/sidebar'
import { StaleBanner } from '@/components/shell/stale-banner'
import { TopBar } from '@/components/shell/top-bar'
import { ShortcutsDialog } from '@/components/shortcuts'
import { UpdateCenter } from '@/components/update-banner'
import { AppBadge, useBadging } from '@/lib/app-badge'
import { useIsMobile } from '@/lib/breakpoints'
import { cn } from '@/lib/utils'
import { useAttentionFeed } from '@/routes/needs-you/attention'
import { ProfileDialog } from '@/routes/members/personal'
import { useTeamRefresh } from '@/routes/team'
import { useStore } from '@/store'

export function AppShell() {
  useNavShortcuts()
  useTeamRefresh()
  useSharedPrompt()
  const profileOpen = useStore((s) => s.profileOpen)
  const setProfileOpen = useStore((s) => s.setProfileOpen)
  const mobile = useIsMobile()
  const badge = useBadging()
  const stacked = useStore((s) => mobile && s.route.name === 'run')
  return (
    <div className={cn('flex h-full min-h-0 flex-col bg-canvas text-ui text-text', stacked && 'pt-[var(--safe-top)]')}>
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
      {!stacked && <TopBar />}
      <StaleBanner />
      <div className="relative flex min-h-0 flex-1 overflow-hidden">
        <Sidebar />
        <main
          id="main"
          tabIndex={-1}
          className={cn('min-h-0 min-w-0 flex-1 overflow-hidden bg-canvas pr-[env(safe-area-inset-right)] outline-none md:pb-[env(safe-area-inset-bottom)]', stacked && 'pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)]')}
        >
          <CenterView />
        </main>
        <AssistantPanel />
      </div>
      {(mobile || badge) && <AttentionFeed />}
      {mobile && <PhoneTabBar />}
      <CommandPalette />
      <PaletteDialogs />
      <ShortcutsDialog />
      <ProfileDialog open={profileOpen} onOpenChange={setProfileOpen} />
      <UpdateCenter />
      <ConnectionAnnouncer />
      {badge && <AppBadge />}
    </div>
  )
}

/** Keeps the attention list fresh for the tab bar and the badge without re-rendering the shell on each change. */
function AttentionFeed() {
  useAttentionFeed()
  return null
}
