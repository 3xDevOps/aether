import { useLayoutEffect, useRef } from 'react'
import type * as React from 'react'
import { assistantLabel, assistantTriggerID, useAssistantAvailable } from '@/components/assistant/button'
import { Bell, Ellipsis, List, Plus, Sparkles } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { canLaunch, launchBlocked } from '@/lib/commands'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { useKeyboardUp } from '@/lib/keyboard-inset'
import { cn, focusRingInset } from '@/lib/utils'
import { useAskingCount, useAttentionSupported } from '@/routes/needs-you/attention'
import { useStore } from '@/store'
import { assistantWaiting } from '@/store/assistant'
import { useCapability, useSelfRole } from '@/store/hooks'

const sessionRoutes = new Set(['sessions', 'board', 'run', 'overview'])

function Tab({ label, icon, active, badge, dot, className, ...props }: Omit<React.ComponentProps<'button'>, 'children'> & {
  label: string
  icon: React.ReactNode
  hint?: React.ReactNode
  active?: boolean
  badge?: number
  dot?: boolean
}) {
  return (
    <li className="flex min-w-0 flex-1">
      <Button
        variant="ghost"
        // A tab that opens a sheet says so with aria-expanded; only a page tab is the current page.
        aria-current={active && props['aria-expanded'] === undefined ? 'page' : undefined}
        className={cn(
          focusRingInset,
          'relative h-auto min-h-14 min-w-0 flex-1 flex-col gap-0.5 px-0 text-ui-xs font-normal coarse:h-auto coarse:px-0 [&_svg:not([class*=size-])]:size-5',
          active && 'font-medium text-accent hover:not-aria-disabled:text-accent',
          className,
        )}
        {...props}
      >
        {active && <span aria-hidden className="absolute inset-x-4 top-0 h-0.5 rounded-full bg-accent" />}
        <span className="relative flex h-7 w-11 items-center justify-center">
          {icon}
          {badge !== undefined && badge > 0 && (
            <span aria-hidden className="absolute -top-1 left-6 inline-flex h-4 min-w-4 items-center justify-center rounded-chip bg-state-needs-you px-1 text-ui-xs leading-none font-semibold text-canvas tabular-nums">
              {badge > 99 ? '99+' : badge}
            </span>
          )}
          {dot && <span aria-hidden className="absolute top-0.5 right-2.5 size-2 rounded-full bg-state-needs-you" />}
        </span>
        <span className="max-w-full truncate">{label}</span>
      </Button>
    </li>
  )
}

// A terminal's own input is left out: hiding the bar would resize the PTY on every focus change.
const typingTarget = 'input:is(:not([type]), [type=text], [type=search], [type=email], [type=url], [type=tel], [type=password], [type=number]), textarea:not(.xterm-helper-textarea), [contenteditable=true]'


/** The phone's navigation, below 768px. Needs you is home; More holds what the desktop sidebar and header carry. */
export function PhoneTabBar() {
  const cap = useCapability()
  const role = useSelfRole()
  const route = useStore((s) => s.route.name)
  const navigate = useStore((s) => s.navigate)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const blocked = useStore((s) => launchBlocked(s.linkStatus))
  const filledLaunch = useStore((s) => s.headerPrimaries === 0)
  const assistantOpen = useStore((s) => s.assistantOpen)
  const setAssistantOpen = useStore((s) => s.setAssistantOpen)
  const assistantWaitingCount = useStore(assistantWaiting)
  const assistantUpdates = useStore((s) => s.assistantNotices)
  const moreOpen = useStore((s) => s.sidebarDrawerOpen)
  const setMoreOpen = useStore((s) => s.setSidebarDrawerOpen)
  const asking = useAskingCount()
  const assistant = useAssistantAvailable()
  const coarse = useMediaQuery(coarsePointer)
  const attention = useAttentionSupported()
  const go = (name: string) => {
    setAssistantOpen(false)
    navigate(name)
  }
  const page = !assistantOpen && !moreOpen
  // A mouse and a hardware keyboard leave the screen alone, so only touch hides the bar.
  const hidden = useKeyboardUp(typingTarget) && coarse
  const bar = useRef<HTMLElement>(null)
  useLayoutEffect(() => {
    const element = bar.current
    if (!element) return
    const root = document.documentElement
    const observer = new ResizeObserver(() => {
      // A hidden bar leaves the toasts to the bottom safe area.
      if (element.offsetHeight > 0) root.style.setProperty('--phone-tabs-height', `${element.offsetHeight}px`)
      else root.style.removeProperty('--phone-tabs-height')
    })
    observer.observe(element)
    return () => {
      observer.disconnect()
      root.style.removeProperty('--phone-tabs-height')
    }
  }, [])

  return (
    <nav ref={bar} aria-label="Main navigation" hidden={hidden || route === 'run'} className="shrink-0 border-t border-seam bg-chrome pr-[env(safe-area-inset-right)] pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)]">
      <ul className="flex items-stretch px-1">
        {attention && (
          <Tab
            label="Needs you"
            aria-label={asking > 0 ? `Needs you, ${asking}` : 'Needs you'}
            icon={<Bell />}
            badge={asking}
            active={page && route === 'needs-you'}
            onClick={() => go('needs-you')}
          />
        )}
        <Tab label="Sessions" icon={<List />} active={page && sessionRoutes.has(route)} onClick={() => go('sessions')} />
        {canLaunch({ cap, role }) && (
          <Tab
            label="New"
            aria-label="New session"
            hint={blocked}
            aria-disabled={blocked ? true : undefined}
            icon={<span className={cn('flex size-full items-center justify-center rounded-full', filledLaunch && 'bg-accent text-on-accent')}><Plus /></span>}
            onClick={() => !blocked && openDialog('launch')}
          />
        )}
        {assistant && (
          <Tab
            id={assistantTriggerID}
            label="Assistant"
            icon={<Sparkles />}
            aria-label={assistantLabel(assistantWaitingCount, assistantUpdates)}
            dot={assistantWaitingCount + assistantUpdates > 0}
            active={assistantOpen}
            aria-expanded={assistantOpen}
            aria-controls="assistant"
            onClick={() => setAssistantOpen(!assistantOpen)}
          />
        )}
        <Tab
          id="sidebar-drawer-trigger"
          label="More"
          aria-label="More pages"
          icon={<Ellipsis />}
          active={moreOpen || (!assistantOpen && !sessionRoutes.has(route) && route !== 'needs-you')}
          aria-expanded={moreOpen}
          aria-controls="sidebar-drawer"
          onClick={() => setMoreOpen(true)}
        />
      </ul>
    </nav>
  )
}
