import { createServer } from 'node:http'
import { parseArgs } from 'node:util'
import httpProxy from 'http-proxy'
import next from 'next'

const { values: cli } = parseArgs({
  options: {
    hostname: { type: 'string', short: 'H' },
    port: { type: 'string', short: 'p' },
  },
  allowPositionals: true,
  strict: false,
})
const port = Number(cli.port ?? process.env.PORT ?? 3000)
const hostname = typeof cli.hostname === 'string' ? cli.hostname : '127.0.0.1'
if (!Number.isInteger(port) || port < 0 || port > 65_535) {
  throw new Error(`port must be an integer from 0 to 65535, got ${port}`)
}

const gateway = process.env.AETHER_DASHBOARD ?? 'http://127.0.0.1:8080'
const gatewayURL = new URL(gateway)
if (gatewayURL.protocol !== 'http:' && gatewayURL.protocol !== 'https:') {
  throw new Error(`AETHER_DASHBOARD must use http or https, got ${gatewayURL.protocol}`)
}

const proxy = httpProxy.createProxyServer({
  target: gateway,
  ws: true,
  // coder/websocket's default Accept policy compares Origin with Host. Keep
  // both browser headers intact so the configured gateway remains the origin
  // security boundary while this local development server proxies the socket.
  changeOrigin: false,
})

function isPath(pathname, prefix) {
  return pathname === prefix || pathname.startsWith(`${prefix}/`)
}

function parseRequestPathname(requestURL) {
  try {
    return new URL(requestURL || '/', 'http://localhost').pathname
  } catch {
    return null
  }
}

function shouldProxy(pathname, prefix) {
  return pathname !== null && isPath(pathname, prefix)
}

const nextInspectorPath = '/__nextjs_attach-nodejs-inspector'

function isInspectorActivation(requestURL) {
  try {
    // Match Next's WHATWG URL parsing, then account for its repeated-slash
    // redirect normalization before the dev middleware chain.
    const pathname = new URL(`http://n${requestURL || '/'}`).pathname
    return pathname.replace(/\/+/g, '/') === nextInspectorPath
  } catch {
    return false
  }
}

function rejectInspectorActivation(response) {
  response.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' })
  response.end('Not Found\n')
}

function rejectMalformedRequest(response) {
  response.writeHead(400, { 'content-type': 'text/plain; charset=utf-8' })
  response.end('Bad Request\n')
}

function proxyHTTP(request, response) {
  proxy.web(request, response, {}, (error) => {
    const detail = error instanceof Error ? error.message : String(error)
    console.error(`Aether gateway proxy failed: ${detail}`)
    if (response.headersSent) {
      response.destroy(error)
      return
    }
    response.writeHead(502, { 'content-type': 'text/plain; charset=utf-8' })
    response.end(`Aether gateway unavailable: ${detail}`)
  })
}

const app = next({ dev: true, hostname, port })

await app.prepare()

const handle = app.getRequestHandler()

const server = createServer((request, response) => {
  const requestURL = request.url || '/'
  const pathname = parseRequestPathname(requestURL)
  if (pathname === null) {
    rejectMalformedRequest(response)
    return
  }
  if (isInspectorActivation(requestURL)) {
    rejectInspectorActivation(response)
    return
  }
  if (shouldProxy(pathname, '/api') || shouldProxy(pathname, '/local')) {
    proxyHTTP(request, response)
    return
  }
  handle(request, response)
})

// The Next request handler installs its upgrade listener on the first request.
// Keep this listener for the gateway path only; forwarding Next upgrades here
// as well would make its HMR WebSocket handler consume each socket twice.
server.on('upgrade', (request, socket, head) => {
  const pathname = parseRequestPathname(request.url || '/')
  if (pathname === null) {
    socket.destroy()
    return
  }
  if (shouldProxy(pathname, '/ws')) {
    proxy.ws(request, socket, head, (error) => {
      if (error) console.error(`Aether gateway WebSocket proxy failed: ${error.message}`)
      socket.destroy()
    })
    return
  }
})

server.listen(port, hostname, () => {
  console.log(`> Aether dashboard Ready on http://${hostname}:${port}`)
})
