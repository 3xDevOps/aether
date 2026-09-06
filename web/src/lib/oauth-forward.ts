import type { Api } from '@/lib/api'

const callbackParameter = /(?:redirect|callback|return)[_-]?(?:uri|url)?/i

function loopbackPort(value: string, direct: boolean): number | null {
  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return null
  }
  if (parsed.protocol !== 'http:') return null
  if (!['localhost', '127.0.0.1', '[::1]'].includes(parsed.hostname)) return null
  if (!direct && !parsed.port) return null
  if (direct && !/(?:auth|oauth).*(?:callback|redirect)/i.test(parsed.pathname)) return null
  const port = Number(parsed.port || '80')
  return Number.isInteger(port) && port >= 1 && port <= 65535 ? port : null
}

/** Finds the loopback callback embedded in an OAuth authorization URL. */
export function oauthCallbackPort(uri: string): number | null {
  const direct = loopbackPort(uri, true)
  if (direct !== null) return direct

  let parsed: URL
  try {
    parsed = new URL(uri)
  } catch {
    return null
  }
  for (const [name, value] of parsed.searchParams) {
    if (!callbackParameter.test(name)) continue
    const port = loopbackPort(value, false)
    if (port !== null) return port
  }
  return null
}

/** Starts the local half of an OAuth callback forward when uri names one. */
export async function forwardOAuthCallback(
  client: Api,
  target: string,
  uri: string,
): Promise<number | null> {
  const port = oauthCallbackPort(uri)
  if (port === null) return null
  await client.localForwardStart(target, port)
  return port
}

/** Opens an OAuth authorization URL only after its callback listener is ready. */
export function openOAuthLink(
  client: Api,
  target: string,
  uri: string,
  onReady: (port: number) => void,
  onError: (error: unknown) => void,
): boolean {
  const port = oauthCallbackPort(uri)
  if (port === null) return false

  const popup = window.open('about:blank', '_blank')
  if (popup) popup.opener = null
  void client.localForwardStart(target, port).then(
    () => {
      onReady(port)
      if (popup) {
        popup.location.replace(uri)
      } else {
        window.open(uri, '_blank', 'noopener,noreferrer')
      }
    },
    (error) => {
      popup?.close()
      onError(error)
    },
  )
  return true
}
