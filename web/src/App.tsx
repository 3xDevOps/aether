import { useEffect } from 'react'
import { Toaster, toast } from 'sonner'
import { AppShell } from '@/components/shell/app-shell'
import { ThemeEffect } from '@/components/theme'
import { useStore } from '@/store'
import { connect } from '@/store/sync'

export function App() {
  const hydrationError = useStore((s) => s.hydrationError)
  const streamDead = useStore((s) => s.streamDead)
  const theme = useStore((s) => s.theme)

  useEffect(() => connect(useStore), [])

  useEffect(() => {
    if (!hydrationError) return
    // A dead token is not an unreachable server; the recorded error already
    // says what happened and how to recover.
    if (streamDead) toast.error(hydrationError)
    else toast.error(`Could not reach the server: ${hydrationError}`)
  }, [hydrationError, streamDead])

  return (
    <>
      <ThemeEffect />
      <AppShell />
      <Toaster position="bottom-right" richColors theme={theme} />
    </>
  )
}
