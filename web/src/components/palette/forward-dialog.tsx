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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  api,
  type LocalForwardStatusResult,
} from '@/lib/api'
import { message } from '@/lib/format'
import { useStore } from '@/store'

export function ForwardDialog() {
  const target = useStore((s) => s.paletteForwardTarget)
  const close = useStore((s) => s.closePaletteDialog)
  const [port, setPort] = useState('1455')
  const [forwards, setForwards] = useState<LocalForwardStatusResult['forwards']>([])
  const [loading, setLoading] = useState(true)
  const [starting, setStarting] = useState(false)
  const [stopping, setStopping] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = async () => {
    if (!target) return
    const result = await api.localForwardStatus()
    setForwards(result.forwards.filter((forward) => forward.target === target))
  }

  useEffect(() => {
    let live = true
    setLoading(true)
    setError(null)
    if (!target) {
      setLoading(false)
      return () => {
        live = false
      }
    }
    api
      .localForwardStatus()
      .then((result) => {
        if (!live) return
        setForwards(result.forwards.filter((forward) => forward.target === target))
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
  }, [target])

  const start = async () => {
    if (!target) return
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
      await api.localForwardStart(target, value)
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
    if (!target) return
    setStopping(forwardPort)
    setError(null)
    try {
      await api.localForwardStop(target, forwardPort)
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
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
        <DialogHeader>
          <DialogTitle>
            {target?.startsWith('run:')
              ? 'Forward a port from this run'
              : 'Forward a port from your environment'}
          </DialogTitle>
          <DialogDescription>
            Makes a port inside the agent's machine reachable at localhost on this computer. This
            is needed for browser logins like codex login (port 1455).
          </DialogDescription>
        </DialogHeader>
        <form
          id="forward-port"
          className="min-h-0 space-y-4 overflow-y-auto -mx-1 px-1"
          onSubmit={(event) => {
            event.preventDefault()
            void start()
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="forward-port-number">Port</Label>
            <Input
              id="forward-port-number"
              autoFocus
              type="number"
              min={1}
              max={65535}
              step={1}
              inputMode="numeric"
              aria-describedby="forward-port-help"
              value={port}
              onChange={(event) => setPort(event.target.value)}
            />
            <p id="forward-port-help" className="text-xs text-muted-foreground">
              Choose the port exposed by the agent. Localhost uses the same port.
            </p>
          </div>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <div className="space-y-2" aria-label="Active forwards">
            <p className="text-xs font-medium uppercase tracking-[0.08em] text-muted-foreground">
              Active forwards
            </p>
            {loading ? (
              <p className="text-sm text-muted-foreground">Loading forwards...</p>
            ) : forwards.length === 0 ? (
              <p className="text-sm text-muted-foreground">No active forwards</p>
            ) : (
              <div className="space-y-2">
                {forwards.map((forward) => (
                  <div
                    key={`${forward.target}:${forward.port}`}
                    className="flex flex-col gap-2 rounded-md border border-border/70 bg-muted/20 p-2.5 sm:flex-row sm:items-center sm:justify-between"
                  >
                    <span className="min-w-0 text-sm">
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
                      className="w-full sm:w-auto"
                    >
                      {stopping === forward.port ? 'Stopping...' : 'Stop'}
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </div>
        </form>
        <DialogFooter className="border-t pt-4">
          <Button variant="outline" onClick={close}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="forward-port"
            disabled={starting || !target}
          >
            {starting ? 'Starting...' : 'Start'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
