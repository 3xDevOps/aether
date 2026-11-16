'use strict'

// The link schemes the app answers to. Old apps only know aether://, so the
// edge keeps writing that; cloudfield:// is the product's new name.
const SCHEMES = ['aether', 'cloudfield']

// The link among a process's argv: Windows and Linux hand one to the app as
// an argument.
function findDeepLink(argv) {
  return argv.find((arg) => SCHEMES.some((scheme) => arg.startsWith(scheme + '://'))) || ''
}

// What a deep link appends to the gateway URL: &run=<id> for
// <scheme>://run/<id>, #join=<link> or #claim=<link> for <scheme>://join?…
// and <scheme>://claim?…, empty for anything else. Every part of a link is
// checked before any of it reaches the gateway URL; anything else is refused
// rather than concatenating attacker-shaped text into it.
function deepLinkSuffix(raw) {
  let parsed
  try {
    parsed = new URL(raw)
  } catch {
    return ''
  }
  if (!SCHEMES.some((scheme) => parsed.protocol === scheme + ':')) return ''
  // <scheme>://run/<id> parses as host "run", pathname "/<id>".
  if (parsed.hostname === 'run') {
    const id = parsed.pathname.replace(/^\//, '')
    // Run IDs are lowercase letters, digits, and hyphens (run-7k2m9q4xbd).
    return /^[0-9a-z-]{10,32}$/.test(id) ? '&run=' + id : ''
  }
  const names =
    parsed.hostname === 'join' ? ['edge', 'server', 'invitation'] : parsed.hostname === 'claim' ? ['edge', 'server'] : null
  if (!names || (parsed.pathname !== '' && parsed.pathname !== '/')) return ''
  const params = parsed.searchParams
  if ([...params.keys()].length !== names.length || names.some((name) => params.getAll(name).length !== 1)) return ''
  let edge
  try {
    edge = new URL(params.get('edge'))
  } catch {
    return ''
  }
  // A relay origin as edgeproto.Origin accepts it: https://host[:port], or
  // http:// for a loopback host, nothing else. URL.origin drops an explicit
  // default port, which edgeproto.Origin keeps.
  const origin = params.get('edge')
  const loopback =
    edge.hostname === 'localhost' ||
    edge.hostname === '[::1]' ||
    /^127\.\d+\.\d+\.\d+$/.test(edge.hostname) ||
    /^\[::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}\]$/.test(edge.hostname)
  const port = edge.protocol === 'https:' ? ':443' : edge.protocol === 'http:' && loopback ? ':80' : null
  if (!port || (origin !== edge.origin && origin !== edge.origin + port)) return ''
  if (!/^[a-z2-7]{26}$/.test(params.get('server'))) return ''
  if (names.includes('invitation') && !/^inv-[0-9a-hjkmnp-tv-z]{10}$/.test(params.get('invitation'))) return ''
  if (!/^#[a-z2-7]{16}$/.test(parsed.hash)) return ''
  // The dashboard reads aether:// links only (web/src/lib/claim-link.ts),
  // and a shell can run beside an older dashboard than its own.
  const link = 'aether:' + parsed.href.slice(parsed.protocol.length)
  return '#' + parsed.hostname + '=' + encodeURIComponent(link)
}

module.exports = { SCHEMES, deepLinkSuffix, findDeepLink }
