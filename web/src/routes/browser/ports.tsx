import { useEffect, useRef, useState } from 'react'
import { Cable, Copy, ExternalLink } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import { EmptyState } from '@/components/ui/empty-state'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { SectionLabel } from '@/components/ui/section-label'
import { Spinner } from '@/components/ui/spinner'
import { api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { message } from '@/lib/format'
import { forwardCommand, openOnThisComputer, portAddress, runOnly, runOnlyReason } from '@/lib/ports'
import type { DevPort } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useCapability } from '@/store/hooks'

const pollEvery = 3000

const nothingListening = 'Nothing in this session is listening on a port yet. Start a server in the Terminal, or ask the agent to, and it appears here.'

export interface Ports {
  /** Null until a read answers, and again after one fails. */
  list: DevPort[] | null
  error: string
}

/** The run's listening ports, re-read every three seconds while the caller
 * is mounted, `live` holds and the tab is visible. Null on a server that
 * cannot list them. */
export function usePorts(runID: string, live: boolean): Ports | null {
  const enabled = useCapability().hasMethod('dev.port.list') && live
  const [ports, setPorts] = useState<Ports>({ list: null, error: '' })
  useEffect(() => {
    if (!enabled) return
    let mounted = true
    let reading = false
    const read = async () => {
      if (reading || document.hidden) return
      reading = true
      try {
        const result = await api.devPortList({ run_id: runID })
        if (mounted) setPorts({ list: result.ports, error: '' })
      } catch (cause) {
        if (mounted) setPorts({ list: null, error: message(cause) })
      } finally { reading = false }
    }
    void read()
    const timer = setInterval(() => void read(), pollEvery)
    return () => {
      mounted = false
      clearInterval(timer)
    }
  }, [runID, enabled])
  return enabled ? ports : null
}

type Detail =
  | { port: number; kind: 'failed' | 'info'; text: string }
  | { port: number; kind: 'forwarded'; url: string }
  | { port: number; kind: 'command' }

function PortList({ runID, ports, available, onOpen }: {
  runID: string
  ports: DevPort[]
  /** False while the run's Browser cannot open a page. */
  available: boolean
  /** Opens an address in the run's Browser. */
  onOpen: (address: string) => void
}) {
  const local = useCapability().hasLocal('forward.start')
  const [detail, setDetail] = useState<Detail | null>(null)
  const [forwarding, setForwarding] = useState<number | null>(null)
  const command = useRef<HTMLElement>(null)

  const outside = (port: DevPort) => {
    if (forwarding !== null) return
    if (runOnly(port)) setDetail({ port: port.port, kind: 'info', text: runOnlyReason(port) })
    else if (!local) setDetail({ port: port.port, kind: 'command' })
    else {
      setDetail(null)
      setForwarding(port.port)
      openOnThisComputer(api, runID, port.port)
        .then(
          (url) => setDetail({ port: port.port, kind: 'forwarded', url }),
          (cause: unknown) => setDetail({ port: port.port, kind: 'failed', text: message(cause) }),
        )
        .finally(() => setForwarding(null))
    }
  }

  return (
    <div className="flex w-80 max-w-full flex-col gap-2">
      <ul aria-label="Listening ports" className="flex min-w-0 flex-col gap-1">
        {ports.map((port) => {
          const address = portAddress(port)
          return (
            <li key={port.port} className="flex min-w-0 items-center gap-1">
              <Button
                variant="secondary"
                className="min-w-0 flex-1 justify-start"
                aria-label={port.process ? `Open ${address}, ${port.process}` : `Open ${address}`}
                aria-disabled={!available || undefined}
                onClick={() => { if (available) onOpen(address) }}
              >
                <span className="font-code">{address}</span>
                {port.process && <span className="min-w-0 truncate font-normal text-muted">{port.process}</span>}
              </Button>
              <Button
                variant="ghost"
                size="icon"
                label={local ? `Open ${port.port} on this computer` : `Open ${port.port} on your own computer`}
                hint={local ? 'Open on this computer' : 'Open on your own computer'}
                aria-disabled={forwarding !== null || undefined}
                onClick={() => outside(port)}
              >
                {forwarding === port.port ? <Spinner /> : <ExternalLink />}
              </Button>
            </li>
          )
        })}
      </ul>
      {detail && (
        <div role={detail.kind === 'failed' ? 'alert' : 'status'} className={cn('text-ui-sm break-words', detail.kind === 'failed' ? 'text-state-failed' : 'text-muted')}>
          {detail.kind === 'forwarded' ? (
            <>
              Port {detail.port} is forwarded to{' '}
              <a href={detail.url} target="_blank" rel="noreferrer" className="text-accent underline-offset-2 hover:underline">localhost:{detail.port}</a>
              {' '}on this computer. To stop it, choose Forward a port… in the session’s More menu.
            </>
          ) : detail.kind === 'command' ? (
            <>
              This dashboard is served by your Aether server, which cannot open a port on your computer. Run this there, then open localhost:{detail.port}.
              <span className="mt-1 block">
                <span className="inline-flex max-w-full items-center gap-1">
                  <Code ref={command}>{forwardCommand(runID, detail.port)}</Code>
                  <Button variant="ghost" size="icon-sm" label="Copy the command" onClick={() => void copyText(forwardCommand(runID, detail.port), command.current)}><Copy /></Button>
                </span>
              </span>
            </>
          ) : detail.text}
        </div>
      )}
    </div>
  )
}

/** The toolbar's count of listening ports and the list behind it. */
export function PortsControl({ runID, ports, available, onOpen }: {
  runID: string
  ports: Ports | null
  available: boolean
  onOpen: (address: string) => void
}) {
  const [open, setOpen] = useState(false)
  if (!ports) return null
  const count = ports.list?.length ?? 0
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="ghost" aria-label={count ? `Ports, ${count} listening` : 'Ports'} hint="Listening ports">
          <Cable />
          <span aria-hidden className="hidden @2xl:inline">Ports</span>
          {count > 0 && <span className="tabular-nums">{count}</span>}
        </Button>
      </PopoverTrigger>
      <PopoverContent data-browser align="end" className="flex w-80 flex-col gap-2">
        <SectionLabel>Listening in this session</SectionLabel>
        {ports.error ? <p role="alert" className="break-words text-state-failed">{ports.error}</p>
          : !ports.list ? <Spinner label="Reading the session’s ports" />
            : ports.list.length === 0 ? <p className="text-muted">{nothingListening}</p>
              : <PortList runID={runID} ports={ports.list} available={available} onOpen={(address) => { setOpen(false); onOpen(address) }} />}
      </PopoverContent>
    </Popover>
  )
}

/** What the pane shows before a page is open: the listening ports first. */
export function OpenAPage({ runID, ports, onOpen }: { runID: string; ports: Ports | null; onOpen: (address: string) => void }) {
  const beside = <>This browser runs on the server beside the session, so <span className="font-code">localhost</span> is the session’s own app, not your machine.</>
  if (ports?.list?.length) {
    return (
      <EmptyState title="Open a page" action={<PortList runID={runID} ports={ports.list} available onOpen={onOpen} />}>
        Pick a port the session is listening on, or type an address above. {beside}
      </EmptyState>
    )
  }
  return (
    <EmptyState title="Open a page">
      {ports?.list ? `${nothingListening} Any other address goes in the field above.` : <>Type an address above and press Enter. {beside}</>}
      {ports?.error && <span role="alert" className="mt-2 block break-words text-state-failed">{ports.error}</span>}
    </EmptyState>
  )
}
