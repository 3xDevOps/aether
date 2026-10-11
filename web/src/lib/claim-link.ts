/** Reads a claim code from a bare code or a pasted claim link:
 * `https://<edge host>/claim/<server id>#<secret>` or
 * `aether://claim?edge=<relay origin>&server=<server id>#<secret>`. A link
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
  if (url.protocol === 'https:') {
    const server = /^\/claim\/([^/]+)$/.exec(url.pathname)?.[1]
    if (server && secret) return { code: `${server}-${secret}`, edge: url.origin }
  }
  if (url.protocol === 'aether:' && url.host === 'claim') {
    const server = url.searchParams.get('server')
    const edge = url.searchParams.get('edge')
    if (server && edge && secret) return { code: `${server}-${secret}`, edge }
  }
  return { code: text }
}
