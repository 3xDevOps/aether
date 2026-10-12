/** Reads a claim code from a bare code or a pasted claim link,
 * `aether://claim?edge=<relay origin>&server=<server id>#<secret>`, which
 * also names the edge to claim through. Anything else is returned as the
 * code, for the server to accept or refuse. */
export function parseClaim(input: string): { code: string; edge?: string } {
  const text = input.trim()
  let url: URL
  try {
    url = new URL(text)
  } catch {
    return { code: text }
  }
  const secret = url.hash.slice(1)
  if (url.protocol === 'aether:' && url.host === 'claim') {
    const server = url.searchParams.get('server')
    const edge = url.searchParams.get('edge')
    if (server && edge && secret) return { code: `${server}-${secret}`, edge }
  }
  return { code: text }
}

/** Reads the edge and ids a join link names:
 * `https://<edge host>/join/<server id>/<invitation id>#<secret>` or
 * `aether://join?edge=<relay origin>&server=<server id>&invitation=<invitation id>#<secret>`.
 * The server checks the secret. Null when the text is neither. */
export function parseJoin(input: string): { edge: string; server: string; invitation: string } | null {
  const text = input.trim()
  let url: URL
  try {
    url = new URL(text)
  } catch {
    return null
  }
  if (!url.hash.slice(1)) return null
  if (url.protocol === 'https:') {
    const ids = /^\/join\/([^/]+)\/([^/]+)$/.exec(url.pathname)
    // URL.origin drops an explicit :443, which edgeproto.Origin keeps.
    const host = /^https:\/\/([^/?#]+)/i.exec(text)?.[1]
    if (ids && host) return { edge: `https://${host.toLowerCase()}`, server: ids[1], invitation: ids[2] }
  }
  if (url.protocol === 'aether:' && url.host === 'join') {
    const edge = url.searchParams.get('edge')
    const server = url.searchParams.get('server')
    const invitation = url.searchParams.get('invitation')
    if (edge && relayOrigin(edge) && server && invitation) return { edge, server, invitation }
  }
  return null
}

/** Whether edge is https://host[:port], or http:// for a loopback host,
 * and nothing else, as edgeproto.Origin and desktop/main.js accept. URL.origin
 * drops an explicit default port, which edgeproto.Origin keeps. */
function relayOrigin(edge: string): boolean {
  try {
    const url = new URL(edge)
    if (url.protocol === 'https:') return edge === url.origin || edge === `${url.origin}:443`
    return url.protocol === 'http:' && loopback(url.hostname) && (edge === url.origin || edge === `${url.origin}:80`)
  } catch {
    return false
  }
}

/** localhost, 127.0.0.0/8, or ::1, also IPv4-mapped, as URL serializes them. */
function loopback(hostname: string): boolean {
  return hostname === 'localhost' || hostname === '[::1]' || /^127\.\d+\.\d+\.\d+$/.test(hostname) ||
    /^\[::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}\]$/.test(hostname)
}
