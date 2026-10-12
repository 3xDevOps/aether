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
  let url: URL
  try {
    url = new URL(input.trim())
  } catch {
    return null
  }
  if (!url.hash.slice(1)) return null
  if (url.protocol === 'https:') {
    const ids = /^\/join\/([^/]+)\/([^/]+)$/.exec(url.pathname)
    if (ids) return { edge: url.origin, server: ids[1], invitation: ids[2] }
  }
  if (url.protocol === 'aether:' && url.host === 'join') {
    const edge = url.searchParams.get('edge')
    const server = url.searchParams.get('server')
    const invitation = url.searchParams.get('invitation')
    if (edge && server && invitation) return { edge, server, invitation }
  }
  return null
}
