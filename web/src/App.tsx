import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { ConnectionError } from '@/components/connection-error'
import { LaunchSplash } from '@/components/launch-splash'
import { AppShell } from '@/components/shell/app-shell'
import { TopBar } from '@/components/shell/top-bar'
import { desktopBridge } from '@/components/shell/window-bar'
import { ThemeEffect } from '@/components/theme'
import { Toaster } from '@/components/ui/toast'
import { useKeyboardInset } from '@/lib/keyboard-inset'
import { bindRouteToUrl } from '@/lib/url-state'
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
  const edge = useStore((s) => s.linkStatus?.edge_url)
  const hydrated = useStore((s) => s.hydrated)
  const gatewayRestarting = useStore((s) => s.gatewayRestarting)
  const epoch = useStore((s) => s.connectionEpoch)
  const resetConnection = useStore((s) => s.resetConnection)
  const drafts = useStore((s) => s.drafts)
  const importPending = useStore((s) => s.configImportPending)
  useEffect(() => {
    if (typeof window === 'undefined') return
    const dirty = Object.values(drafts).some((draft) => draft.content !== draft.baseContent || draft.saving)
    if (!dirty && !importPending) return
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [drafts, importPending])
  const theme = useStore((s) => s.theme)
  // A retry remounts the connection effect; a page reload would lose the
  // in-memory session token.
  const [attempt, setAttempt] = useState(0)
  useKeyboardInset()

  useEffect(() => connect(useStore), [attempt, epoch])
  useEffect(() => bindRouteToUrl(useStore), [])

  // An in-app update restarts the gateway on purpose, so it is not a total failure.
  const blocked = !hydrated && hydrationError !== null && !gatewayRestarting

  useEffect(() => {
    if (!hydrationError || blocked) return
    // A dead token is not an unreachable server; its error already says how to recover.
    if (streamDead) toast.error(hydrationError)
    else toast.error(`Could not reach the server: ${hydrationError}`)
  }, [hydrationError, streamDead, blocked])

  const retry = useCallback(() => {
    resetConnection()
    setAttempt((n) => n + 1)
  }, [resetConnection])

  return (
    <>
      <ThemeEffect />
      <LaunchSplash />
      <div className="flex min-w-0 h-full flex-col">
        {blocked && desktopBridge() && <TopBar navigation={false} />}
        <div className="min-h-0 min-w-0 flex-1">
          {blocked ? (
            <ConnectionError
              kind={unreachable}
              dead={streamDead}
              error={hydrationError}
              edge={edge}
              onRetry={retry}
            />
          ) : (
            <>
              <AppShell />
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
            </>
          )}
        </div>
      </div>
    </>
  )
}
