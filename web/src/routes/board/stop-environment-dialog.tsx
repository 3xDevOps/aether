import { useState } from 'react'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { useStore } from '@/store'

export function StopEnvironmentDialog({
  client,
  onClose,
  onStopped,
}: {
  client: Api
  onClose: () => void
  onStopped?: () => void
}) {
  const reset = useStore((s) => s.resetEnvTerminal)
  const setStatus = useStore((s) => s.setEnvTerminalStatus)
  const [stopping, setStopping] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const stop = async () => {
    if (stopping) return
    setStopping(true)
    setError(null)
    try {
      await client.terminalStop()
      const status = useStore.getState().envTerminal.status
      onStopped?.()
      onClose()
      reset()
      setStatus({ ...status, running: false, tabs: [] })
    } catch (err) {
      setError(message(err))
    } finally {
      setStopping(false)
    }
  }

  return (
    <AlertDialog
      open
      onOpenChange={(open) => {
        // A failed stop is reported in here, so Escape stays off until
        // the call settles.
        if (!stopping && !open) onClose()
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Stop your environment?</AlertDialogTitle>
          <AlertDialogDescription>
            The environment container stops now, and so does everything
            running in it. Your home files and your saved image remain, and a
            later open starts it again.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="text-sm text-state-failed">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={stopping}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            variant="primary"
            onClick={(event) => {
              event.preventDefault()
              void stop()
            }}
            disabled={stopping}
          >
            {stopping ? 'Stopping...' : 'Stop environment'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
