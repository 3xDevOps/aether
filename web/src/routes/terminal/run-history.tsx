import { useEffect, useRef, useState } from 'react'
import type * as AsciinemaPlayer from 'asciinema-player'
import type { Player } from 'asciinema-player'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { message } from '@/lib/format'

// asciinema-player emits 'error' but omits it from its declarations.
declare module 'asciinema-player' {
  interface Player {
    addEventListener(
      eventName: 'error',
      handler: (this: Player, event: { name: string; message: string }) => void,
    ): void
  }
}

type PlayerModule = typeof AsciinemaPlayer

type RunHistoryProps = {
  runID: string
}

export function RunHistory({ runID }: RunHistoryProps) {
  const [open, setOpen] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [recording, setRecording] = useState<string | null>(null)
  const [playerModule, setPlayerModule] = useState<PlayerModule | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const playerHost = useRef<HTMLDivElement | null>(null)
  const player = useRef<Player | null>(null)

  useEffect(() => {
    if (!open) return

    const controller = new AbortController()
    let active = true
    setRecording(null)
    setPlayerModule(null)
    setError(null)
    setLoading(true)

    void Promise.all([
      api.runRecording(runID, controller.signal),
      import('asciinema-player'),
      import('asciinema-player/dist/bundle/asciinema-player.css'),
    ])
      .then(([cast, module]) => {
        if (!active || controller.signal.aborted) return
        setRecording(cast)
        setPlayerModule(module)
        setLoading(false)
      })
      .catch((cause: unknown) => {
        if (!active || controller.signal.aborted) return
        setLoading(false)
        setError(message(cause))
      })

    return () => {
      active = false
      controller.abort()
      const current = player.current
      player.current = null
      current?.dispose()
    }
  }, [attempt, open, runID])

  useEffect(() => {
    if (!open || recording === null || !playerModule || !playerHost.current) return

    let active = true
    let instance: Player
    try {
      instance = playerModule.create(
        { data: () => recording },
        playerHost.current,
        { preload: true, autoPlay: false, fit: 'both' },
      )
    } catch (cause: unknown) {
      setError(message(cause))
      return
    }

    player.current = instance
    instance.addEventListener('error', (event) => {
      if (!active) return
      setError(event.message)
      if (player.current === instance) {
        player.current = null
        instance.dispose()
      }
    })

    return () => {
      active = false
      if (player.current !== instance) return
      player.current = null
      instance.dispose()
    }
  }, [open, playerModule, recording])

  const close = () => {
    setOpen(false)
    setRecording(null)
    setPlayerModule(null)
    setLoading(false)
    setError(null)
  }
  const onDialogOpenChange = (nextOpen: boolean) => {
    if (nextOpen) {
      setOpen(true)
      return
    }
    close()
  }
  const retry = () => {
    setAttempt((value) => value + 1)
  }
  const seekBeginning = () => {
    const current = player.current
    if (!current) return
    void current.seek('0%').catch((cause: unknown) => {
      if (player.current !== current) return
      player.current = null
      current.dispose()
      setError(message(cause))
    })
  }

  return (
    <>
      <Button
        size="sm"
        variant="outline"
        aria-label="Open complete terminal history"
        onClick={() => setOpen(true)}
      >
        History
      </Button>
      <Dialog open={open} onOpenChange={onDialogOpenChange}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] sm:max-w-[min(1100px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0">
          <DialogHeader className="min-w-0 border-b px-3 py-3 pr-10 sm:px-4">
            <DialogTitle>Complete terminal history</DialogTitle>
            <DialogDescription>
              Live terminal view keeps recent scrollback. This recording includes the complete
              transcript, including screens that full-screen TUIs overwrote. It opens at the
              beginning; use the player timeline to seek through it without changing the live
              terminal.
            </DialogDescription>
          </DialogHeader>
          <div className="min-h-0 overflow-auto px-3 py-3 sm:px-4">
            {loading && (
              <p className="py-10 text-center text-sm text-muted-foreground" role="status">
                Loading complete terminal history…
              </p>
            )}
            {error && (
              <div
                className="rounded-[2px] border border-state-failed/40 bg-state-failed/10 p-3 text-sm text-[var(--danger-soft-foreground)]"
                role="alert"
              >
                {error}
              </div>
            )}
            {recording !== null && !error && (
              <div
                ref={playerHost}
                className="h-[min(60dvh,520px)] w-full overflow-hidden rounded-[2px] bg-black"
              />
            )}
          </div>
          <DialogFooter className="flex-wrap border-t px-3 py-3 sm:px-4">
            {error && (
              <Button variant="outline" onClick={retry}>
                Retry
              </Button>
            )}
            {recording !== null && !error && (
              <Button variant="outline" onClick={seekBeginning}>
                Beginning
              </Button>
            )}
            <Button variant="outline" onClick={close}>
              Back to live
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
