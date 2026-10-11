import type { Api } from '@/lib/api'
import type { DevPort } from '@/lib/types'

/** True for a port bound to the run's loopback alone. The run's Browser
 * reaches it; a forward arrives from outside the run and cannot. */
export function runOnly(port: DevPort): boolean {
  return port.address === '::1' || port.address.startsWith('127.')
}

export function runOnlyReason(port: DevPort): string {
  return `Port ${port.port} is bound to ${port.address} inside the run, so only the run can reach it. Start the server on 0.0.0.0 to open it on this computer.`
}

/** The address that reaches the port from inside the run. */
export function portAddress(port: DevPort): string {
  const everywhere = port.address === '0.0.0.0' || port.address === '::'
  const host = everywhere || runOnly(port) ? 'localhost' : port.address.includes(':') ? `[${port.address}]` : port.address
  return `${host}:${port.port}`
}

/** What forwards the port to the machine it is typed on. */
export function forwardCommand(runID: string, port: number): string {
  return `aether forward ${runID} ${port}`
}

/** Forwards a run's port to this computer and opens it in the person's own
 * browser. Call it from the click itself: the tab has to be opened before the
 * first await or a popup blocker refuses it. Resolves with the address opened
 * and rejects with the reason nothing was. */
export async function openOnThisComputer(client: Pick<Api, 'devPortList' | 'localForwardStart'>, runID: string, port: number): Promise<string> {
  const url = `http://localhost:${port}`
  const tab = window.open('about:blank', '_blank')
  if (tab) tab.opener = null
  try {
    const { ports } = await client.devPortList({ run_id: runID })
    const found = ports.find((item) => item.port === port)
    if (!found) throw new Error(`Nothing in the run is listening on port ${port}.`)
    if (runOnly(found)) throw new Error(runOnlyReason(found))
    await client.localForwardStart(`run:${runID}`, port)
  } catch (cause) {
    tab?.close()
    throw cause
  }
  if (tab) tab.location.replace(url)
  else window.open(url, '_blank', 'noopener,noreferrer')
  return url
}
