import { useCallback, useEffect, useState } from 'react'
import { Toaster, toast } from 'sonner'
import { ConnectionError } from '@/components/connection-error'
import { LaunchSplash } from '@/components/launch-splash'
import { AppShell } from '@/components/shell/app-shell'
import { TitleBar } from '@/components/shell/title-bar'
import { ThemeEffect } from '@/components/theme'
import { useStore } from '@/store'
import { connect } from '@/store/sync'

/** Clear of the status bar and of the home indicator below it. */
const toastOffset = {
  bottom: 'calc(var(--status-bar-height) + 8px + env(safe-area-inset-bottom))',
  right: 'calc(8px + env(safe-area-inset-right))',
}

export function App() {
  const hydrationError = useStore((s) => s.hydrationError)
  const streamDead = useStore((s) => s.streamDead)
  const unreachable = useStore((s) => s.unreachable)
  const hydrated = useStore((s) => s.hydrated)
  const gatewayRestarting = useStore((s) => s.gatewayRestarting)
  const epoch = useStore((s) => s.connectionEpoch)
  const resetConnection = useStore((s) => s.resetConnection)
  const drafts = useStore((s) => s.drafts)
  const importPending = useStore((s) => s.onboardingImportPending)
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
  // Bumping this remounts the connection effect, which is what a retry is:
  // a fresh subscribe and hydrate, not a page reload that would lose the
  // session token held in memory.
  const [attempt, setAttempt] = useState(0)

  useEffect(() => connect(useStore), [attempt, epoch])

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
      <ThemeEffect />
      <LaunchSplash />
      {/* The desktop window is frameless, so the title bar has to outrank the
          branch below it: without it the error page would leave an offline user
          no way to move or close the window. */}
      <div className="flex min-w-0 h-full flex-col">
        <TitleBar commandPaletteDisabled={blocked} />
        <div className="min-h-0 min-w-0 flex-1">
          {blocked ? (
            <ConnectionError
              kind={unreachable}
              dead={streamDead}
              error={hydrationError}
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
                // Above the status bar, whatever height the pointer gives
                // it, and clear of the home indicator; the inset is 0 on a
                // device without one. Sonner swaps to `mobileOffset` under
                // 600px and falls back to its own 16px default when none is
                // given, which is inside the bar on a phone, so both take
                // the same value.
                offset={toastOffset}
                mobileOffset={toastOffset}
                toastOptions={{
                  className:
                    'rounded-[4px] border border-border bg-popover px-3 py-2 text-[13px] text-popover-foreground shadow-overlay',
                  classNames: {
                    title: 'font-medium',
                    description: 'text-muted-foreground',
                  },
                }}
              />
            </>
          )}
        </div>
      </div>
    </>
  )
}
