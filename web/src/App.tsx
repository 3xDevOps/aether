import { useCallback, useEffect, useState } from 'react'
import { Toaster, toast } from 'sonner'
import { ConnectionError } from '@/components/connection-error'
import { LaunchSplash } from '@/components/launch-splash'
import { AppShell } from '@/components/shell/app-shell'
import { TitleBar } from '@/components/shell/title-bar'
import { ThemeEffect } from '@/components/theme'
import { useStore } from '@/store'
import { connect } from '@/store/sync'

export function App() {
  const hydrationError = useStore((s) => s.hydrationError)
  const streamDead = useStore((s) => s.streamDead)
  const unreachable = useStore((s) => s.unreachable)
  const hydrated = useStore((s) => s.hydrated)
  const gatewayRestarting = useStore((s) => s.gatewayRestarting)
  const resetConnection = useStore((s) => s.resetConnection)
  const theme = useStore((s) => s.theme)
  // Bumping this remounts the connection effect, which is what a retry is:
  // a fresh subscribe and hydrate, not a page reload that would lose the
  // session token held in memory.
  const [attempt, setAttempt] = useState(0)

  useEffect(() => connect(useStore), [attempt])

  // Nothing has loaded and the failure is total: the page below says what
  // broke and how to fix it, and there is no shell left to toast over. An
  // in-app update makes the gateway exit and come back on purpose, so that
  // is not this failure even when it briefly looks like one.
  const blocked = !hydrated && hydrationError !== null && !gatewayRestarting

  useEffect(() => {
    if (!hydrationError || blocked) return
    // A dead token is not an unreachable server; the recorded error already
    // says what happened and how to recover.
    if (streamDead) toast.error(hydrationError)
    else toast.error(`Could not reach the server: ${hydrationError}`)
  }, [hydrationError, streamDead, blocked])

  const retry = useCallback(() => {
    resetConnection()
    setAttempt((n) => n + 1)
  }, [resetConnection])

  return (
    <>
      <LaunchSplash />
      {/* The desktop window is frameless, so the title bar has to outrank the
          branch below it: without it the error page would leave an offline user
          no way to move or close the window. */}
      <div className="flex h-full flex-col">
        <TitleBar />
        <div className="min-h-0 flex-1">
          {blocked ? (
            <ConnectionError
              kind={unreachable}
              dead={streamDead}
              error={hydrationError}
              onRetry={retry}
            />
          ) : (
            <>
              <ThemeEffect />
              <AppShell />
              <Toaster position="bottom-right" richColors theme={theme} />
            </>
          )}
        </div>
      </div>
    </>
  )
}
