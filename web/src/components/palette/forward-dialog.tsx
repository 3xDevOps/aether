import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  api,
  type LocalForwardStatusResult,
} from '@/lib/api'
import { message } from '@/lib/format'
import { useStore } from '@/store'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

export function ForwardDialog() {
  const runID = useStore((s) => s.paletteRunID)
  const close = useStore((s) => s.closePaletteDialog)
  const [port, setPort] = useState('1455')
  const [forwards, setForwards] = useState<LocalForwardStatusResult['forwards']>([])
  const [loading, setLoading] = useState(true)
  const [starting, setStarting] = useState(false)
  const [stopping, setStopping] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = async () => {
    if (!runID) return
    const result = await api.localForwardStatus()
    setForwards(result.forwards.filter((forward) => forward.run_id === runID))
  }

  useEffect(() => {
    let live = true
    setLoading(true)
    setError(null)
    if (!runID) {
      setLoading(false)
      return () => {
        live = false
      }
    }
    api
      .localForwardStatus()
      .then((result) => {
        if (!live) return
        setForwards(result.forwards.filter((forward) => forward.run_id === runID))
      })
      .catch((err) => {
        if (!live) return
        const detail = `Forward status failed: ${message(err)}`
        setError(detail)
        toast.error(detail)
      })
      .finally(() => {
        if (live) setLoading(false)
      })
    return () => {
      live = false
    }
  }, [runID])

  const start = async () => {
    if (!runID) return
    const value = Number(port)
    if (!Number.isInteger(value) || value < 1 || value > 65535) {
      const detail = 'Port must be between 1 and 65535'
      setError(detail)
      toast.error(detail)
      return
    }
    setStarting(true)
    setError(null)
    try {
      await api.localForwardStart(runID, value)
      await refresh()
      toast.success('Port forwarding started')
    } catch (err) {
      const detail = `Forward failed: ${message(err)}`
      setError(detail)
      toast.error(detail)
    } finally {
      setStarting(false)
    }
  }

  const stop = async (forwardPort: number) => {
    if (!runID) return
    setStopping(forwardPort)
    setError(null)
    try {
      await api.localForwardStop(runID, forwardPort)
      await refresh()
      toast.success('Port forwarding stopped')
    } catch (err) {
      const detail = `Stop failed: ${message(err)}`
      setError(detail)
      toast.error(detail)
    } finally {
      setStopping(null)
    }
  }

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Forward a port</DialogTitle>
          <DialogDescription>
            Makes a port inside the agent's machine reachable at localhost on this computer - needed for browser logins like codex login (port 1455).
          </DialogDescription>
        </DialogHeader>
        <form
          id="forward-port"
          className="space-y-3"
          onSubmit={(event) => {
            event.preventDefault()
            void start()
          }}
        >
          <label className="block space-y-1 text-sm">
            Port
            <input
              autoFocus
              type="number"
              min={1}
              max={65535}
              step={1}
              inputMode="numeric"
              className={field}
              value={port}
              onChange={(event) => setPort(event.target.value)}
            />
          </label>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <div className="space-y-2" aria-label="Active forwards">
            {loading ? (
              <p className="text-sm text-muted-foreground">Loading forwards...</p>
            ) : forwards.length === 0 ? (
              <p className="text-sm text-muted-foreground">No active forwards</p>
            ) : (
              forwards.map((forward) => (
                <div
                  key={`${forward.run_id}:${forward.port}`}
                  className="flex items-center justify-between gap-3 text-sm"
                >
                  <span>
                    <span className="font-medium">Port {forward.port}</span>{' '}
                    <span className="text-muted-foreground">
                      localhost:{forward.local_port} ({forward.conns} connections)
                    </span>
                  </span>
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => void stop(forward.port)}
                    disabled={stopping !== null}
                  >
                    {stopping === forward.port ? 'Stopping...' : 'Stop'}
                  </Button>
                </div>
              ))
            )}
          </div>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={close}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="forward-port"
            disabled={starting || !runID}
          >
            {starting ? 'Starting...' : 'Start'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
