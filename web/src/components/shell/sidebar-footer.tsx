import { useState } from 'react'
import { ArrowUpCircle, Keyboard, User } from '@/components/icons'
import { ConnectionDot, connectionLabel } from '@/components/shell/connection'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import {
  Menu,
  MenuContent,
  MenuItem,
  MenuLabel,
  MenuRadioGroup,
  MenuRadioItem,
  MenuSeparator,
  MenuSub,
  MenuSubContent,
  MenuSubTrigger,
  MenuTrigger,
} from '@/components/ui/menu'
import { useUpdateNotice } from '@/components/update-banner'
import { useIsMobile } from '@/lib/breakpoints'
import { ProfileDialog } from '@/routes/members/personal'
import { TeamSummary } from '@/routes/team/budget'
import { useStore } from '@/store'
import type { Theme } from '@/store/ui'

const themes: { value: Theme; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

export function UpdateNotice() {
  const notice = useUpdateNotice()
  const open = useStore((s) => s.setUpdatesOpen)
  if (!notice) return null
  return (
    <p role="status" title={notice.text} className="flex min-w-0 items-baseline gap-1 px-4 py-1 text-ui-sm text-muted">
      <span className="min-w-0 truncate">{notice.text}</span>
      {notice.action && (
        <span className="shrink-0">
          {'· '}
          <Button variant="link" onClick={() => open(true)}>Update</Button>
        </span>
      )}
    </p>
  )
}

export function SidebarFooter() {
  const self = useStore((s) => s.info?.member)
  const color = useStore((s) => (s.info ? s.members[s.info.member.id]?.color : undefined))
  const connection = useStore((s) => s.connection)
  const theme = useStore((s) => s.theme)
  const setTheme = useStore((s) => s.setTheme)
  const openShortcuts = useStore((s) => s.setShortcutsOpen)
  const openUpdates = useStore((s) => s.setUpdatesOpen)
  const clearDismissed = useStore((s) => s.clearDismissedUpdates)
  const update = useUpdateNotice(true)
  const mobile = useIsMobile()
  const [profile, setProfile] = useState(false)
  const name = self?.display_name ?? 'Not signed in'
  const word = connectionLabel[connection]

  const themeItems = (
    <MenuRadioGroup value={theme} onValueChange={(value) => setTheme(value as Theme)}>
      {themes.map(({ value, label }) => (
        <MenuRadioItem key={value} value={value}>{label}</MenuRadioItem>
      ))}
    </MenuRadioGroup>
  )

  return (
    <div className="shrink-0 border-t border-seam p-2">
      <Menu>
        <MenuTrigger asChild>
          <Button variant="ghost" hint={word} aria-label={`${name}, ${word}`} className="w-full justify-start">
            {self ? (
              <Avatar name={name} color={color} size="header" />
            ) : (
              <span aria-hidden className="grid size-5 shrink-0 place-items-center rounded-full border-[1.5px] border-seam bg-chrome text-muted">
                <User className="size-3" />
              </span>
            )}
            <span className="min-w-0 flex-1 truncate text-left text-text">{name}</span>
            <ConnectionDot />
          </Button>
        </MenuTrigger>
        <MenuContent side="top" align="start" className="w-60">
          {self && (
            <MenuItem onSelect={() => setProfile(true)}>
              <User />
              Profile
            </MenuItem>
          )}
          <MenuItem onSelect={() => openShortcuts(true)}>
            <Keyboard />
            Keyboard shortcuts
          </MenuItem>
          {mobile ? (
            <>
              <MenuLabel>Theme</MenuLabel>
              {themeItems}
            </>
          ) : (
            <MenuSub>
              <MenuSubTrigger>Theme</MenuSubTrigger>
              <MenuSubContent>{themeItems}</MenuSubContent>
            </MenuSub>
          )}
          {update?.action && (
            <MenuItem
              onSelect={() => {
                clearDismissed()
                openUpdates(true)
              }}
            >
              <ArrowUpCircle />
              Update…
            </MenuItem>
          )}
          <TeamSummary heading={<><MenuSeparator /><MenuLabel>Team</MenuLabel></>} />
        </MenuContent>
      </Menu>
      <ProfileDialog open={profile} onOpenChange={setProfile} />
    </div>
  )
}
