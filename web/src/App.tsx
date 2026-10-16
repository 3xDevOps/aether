import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { ConnectionError } from '@/components/connection-error'
import { LaunchSplash } from '@/components/launch-splash'
import { AppShell } from '@/components/shell/app-shell'
import { TopBar } from '@/components/shell/top-bar'
import { desktopBridge } from '@/components/shell/window-bar'
import { ThemeEffect } from '@/components/theme'
import { Toaster } from '@/components/ui/toast'
import { UpdateChecks } from '@/components/update-checks'
import { UpdateGate } from '@/components/update-gate'
import { useKeyboardInset } from '@/lib/keyboard-inset'
import { bindRouteToUrl, handedLink, onHandedLink, onHandedLinkForgotten } from '@/lib/url-state'
import { useStore } from '@/store'
import { connect } from '@/store/sync'

const toastOffset = {
  bottom: 'calc(8px + env(safe-area-inset-bottom))',
  right: 'calc(8px + env(safe-area-inset-right))',
}

export function App() {
  const hydrationError = useStore((s) => s.hydrationError)
  const streamDead = useStore((s) => s.streamDead)
  const unreachable = useStore((s) => s.unreachable)
  const approvalCode = useStore((s) => s.approvalCode)
  const edge = useStore((s) => s.linkStatus?.edge_url)
  const hydrated = useStore((s) => s.hydrated)
  const gatewayRestarting = useStore((s) => s.gatewayRestarting)
  const epoch = useStore((s) => s.connectionEpoch)
  const resetConnection = useStore((s) => s.resetConnection)
  const drafts = useStore((s) => s.drafts)
  const onboardingRoute = useStore((s) => s.route.name === 'onboarding')
  // The server enforces the floor; this only explains it, and it outranks
  // every other screen because the server refuses everything else.
  const gated = useStore((s) => s.updateRequired !== null || s.update?.client_update_required === true)
  const [handed, setHanded] = useState(handedLink)
  useEffect(() => onHandedLink(setHanded), [])
  useEffect(() => onHandedLinkForgotten(() => setHanded(null)), [])
  useEffect(() => {
    if (typeof window === 'undefined') return
    const dirty = Object.values(drafts).some((draft) => draft.content !== draft.baseContent || draft.saving)
    if (!dirty) return
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [drafts])
  const theme = useStore((s) => s.theme)
  // A retry remounts the connection effect; a page reload would lose the
  // in-memory session token.
  const [attempt, setAttempt] = useState(0)
  useKeyboardInset()
  // Browsers match :focus-visible after any key press followed by a scripted
  // focus move (Enter to send, a shortcut, a closing dialog); focus outlines
  // also need `data-tabbing`, which Tab sets and the next pointer press clears.
  useEffect(() => {
    const root = document.documentElement
    const key = (event: KeyboardEvent) => {
      if (event.key === 'Tab') root.dataset.tabbing = ''
    }
    const pointer = () => {
      delete root.dataset.tabbing
    }
    window.addEventListener('keydown', key)
    window.addEventListener('pointerdown', pointer, true)
    return () => {
      window.removeEventListener('keydown', key)
      window.removeEventListener('pointerdown', pointer, true)
      delete root.dataset.tabbing
    }
  }, [])

  useEffect(() => connect(useStore), [attempt, epoch])
  useEffect(() => bindRouteToUrl(useStore), [])

  // An in-app update restarts the gateway on purpose, so it is not a total failure.
  // A handed link's Connect step talks only to the local gateway, so a saved
  // server that cannot answer must not hide it.
  const blocked = !hydrated && hydrationError !== null && !gatewayRestarting && !(handed && onboardingRoute)

  useEffect(() => {
    if (!hydrationError || blocked || gated) return
    // A dead token is not an unreachable server; its error already says how to recover.
    if (streamDead) toast.error(hydrationError)
    else toast.error(`Could not reach the server: ${hydrationError}`)
  }, [hydrationError, streamDead, blocked, gated])

  const retry = useCallback(() => {
    resetConnection()
    setAttempt((n) => n + 1)
  }, [resetConnection])

  const toaster = (
    <Toaster
      position="bottom-right"
      theme={theme}
      expand={false}
      visibleToasts={4}
      gap={4}
      // Sonner uses `mobileOffset` under 600px and its own default without one.
      offset={toastOffset}
      mobileOffset={toastOffset}
    />
  )

  return (
    <>
      <ThemeEffect />
      <LaunchSplash />
      <UpdateChecks />
      <div className="flex min-w-0 h-full flex-col">
        {(blocked || gated) && desktopBridge() && <TopBar navigation={false} />}
        <div className="min-h-0 min-w-0 flex-1">
          {gated ? (
            <>
              <UpdateGate />
              {toaster}
            </>
          ) : blocked ? (
            <ConnectionError
              kind={unreachable}
              dead={streamDead}
              error={hydrationError}
              edge={edge}
              approvalCode={approvalCode}
              onRetry={retry}
            />
          ) : (
            <>
              <AppShell />
              {toaster}
            </>
          )}
        </div>
      </div>
    </>
  )
}
