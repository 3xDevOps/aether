const scheme = /^[a-z][a-z0-9+.-]*:\/\//i
const local = /^(?:localhost|[^/?#:@]+\.localhost|\d{1,3}(?:\.\d{1,3}){3}|\[[0-9a-f:.]+\])(?::\d+)?(?:[/?#]|$)/i
const runLocalHosts = ['localhost', '127.0.0.1', '0.0.0.0', '[::1]']

/** What the address bar sends for what was typed. The server takes absolute
 * URLs only, so an address without a scheme gets one: HTTP for localhost and
 * IP literals, which rarely serve TLS, HTTPS for every other host. */
export function normalizeAddress(input: string): string {
  const text = input.trim()
  if (!text || text === 'about:blank' || scheme.test(text)) return text
  return `${local.test(text) ? 'http' : 'https'}://${text}`
}

/** True for an http(s) link to the machine that printed it, which for a run's
 * terminal is the run and not the viewer's device. */
export function isRunLocal(uri: string): boolean {
  let url: URL
  try {
    url = new URL(uri)
  } catch {
    return false
  }
  return (url.protocol === 'http:' || url.protocol === 'https:') && runLocalHosts.includes(url.hostname)
}
