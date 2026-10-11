import { useEffect, useState } from 'react'
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
import { errorSentence } from '@/lib/format'
import { useReturnFocus } from '@/lib/hooks'
import { runOnly, runOnlyReason } from '@/lib/ports'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

export function ForwardDialog() {
  const returnFocus = useReturnFocus()
  const target = useStore((s) => s.paletteForwardTarget)
  const close = useStore((s) => s.closePaletteDialog)
  const cap = useCapability()
  const [port, setPort] = useState('')
  const [forwards, setForwards] = useState<LocalForwardStatusResult['forwards']>([])
  const [loading, setLoading] = useState(true)
  const [starting, setStarting] = useState(false)
  const [stopping, setStopping] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [silent, setSilent] = useState<number | null>(null)

  const runID = target?.startsWith('run:') ? target.slice('run:'.length) : null
  const place = runID ? 'the run' : 'your environment'

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
        if (live) setError(`Forward status failed: ${errorSentence(err)}`)
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
      setError('Port must be between 1 and 65535')
      return
    }
    setStarting(true)
    setError(null)
    setSilent(null)
    try {
      if (runID && cap.hasMethod('dev.port.list')) {
        const { ports } = await api.devPortList({ run_id: runID })
        const listening = ports.find((item) => item.port === value)
        if (listening && runOnly(listening)) throw new Error(runOnlyReason(listening))
        if (!listening) setSilent(value)
      }
      await api.localForwardStart(target, value)
      await refresh()
    } catch (err) {
      setSilent(null)
      setError(`Forward failed: ${errorSentence(err)}`)
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
    } catch (err) {
      setError(`Stop failed: ${errorSentence(err)}`)
    } finally {
      setStopping(null)
    }
  }

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent {...returnFocus} className="max-h-[calc(100dvh-2rem)] max-w-[min(520px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0">
        <DialogHeader className="min-w-0 border-b px-3 py-3 pr-10 sm:px-4">
          <DialogTitle>
            {runID ? 'Forward a port from this run' : 'Forward a port from your environment'}
          </DialogTitle>
          <DialogDescription>
            Opens the same port on this computer and carries its connections to {place}, until you stop it or quit Aether.
          </DialogDescription>
        </DialogHeader>
        <form
          id="forward-port"
          className="min-h-0 min-w-0 space-y-3 overflow-y-auto px-3 py-3 sm:px-4"
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
            <p id="forward-port-help" className="text-ui-sm leading-4 text-muted">
              A forward reaches a server listening on 0.0.0.0, not one bound to 127.0.0.1.
            </p>
          </div>
          {error && (
            <p role="alert" className="break-words text-ui-sm text-state-failed">
              {error}
            </p>
          )}
          {silent !== null && (
            <p role="status" className="text-ui-sm text-muted">
              Nothing in the run is listening on port {silent} yet. Its link works once something does.
            </p>
          )}
          <div className="space-y-1.5" aria-label="Forwarded ports">
            <p className="text-ui-sm font-medium text-muted">Forwarded ports</p>
            {loading ? (
              <p className="text-ui text-muted">Loading forwards…</p>
            ) : forwards.length === 0 ? (
              <p className="text-ui text-muted">None yet</p>
            ) : (
              <div className="divide-y divide-seam/70 border-y border-seam/70">
                {forwards.map((forward) => (
                  <div
                    key={`${forward.target}:${forward.port}`}
                    className="flex min-w-0 flex-wrap items-center justify-between gap-2 px-2 py-2"
                  >
                    <span className="min-w-0 flex-1 break-words text-ui">
                      <a
                        href={`http://localhost:${forward.local_port}`}
                        target="_blank"
                        rel="noreferrer"
                        className="font-code text-accent underline-offset-2 hover:underline"
                      >
                        localhost:{forward.local_port}
                      </a>{' '}
                      <span className="text-muted">
                        {forward.conns === 1 ? '1 connection' : `${forward.conns} connections`}
                      </span>
                    </span>
                    <Button
                      type="button"
                      variant="secondary"
                      size="sm"
                      onClick={() => void stop(forward.port)}
                      disabled={stopping !== null}
                    >
                      {stopping === forward.port ? 'Stopping…' : 'Stop'}
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </div>
        </form>
        <DialogFooter className="border-t px-3 py-3 sm:px-4">
          <Button variant="secondary" onClick={close}>
            Close
          </Button>
          <Button
            type="submit"
            form="forward-port"
            disabled={starting || !target}
          >
            {starting ? 'Forwarding…' : 'Forward'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
