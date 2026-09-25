import http from 'node:http';
import { chmod, lstat, mkdir, unlink } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { BrowserSession, BrowserError, limits, requireValue } from './session.mjs';
import { streamPage } from './stream.mjs';
import { renderTerminal } from './terminal.mjs';

async function readJSON(request) {
  let length = 0;
  const chunks = [];
  for await (const chunk of request) {
    length += chunk.length;
    requireValue(length <= limits.request, 'Request exceeds byte limit', 'resource_limit');
    chunks.push(chunk);
  }
  try { return JSON.parse(Buffer.concat(chunks).toString('utf8')); }
  catch { throw new BrowserError('invalid_request', 'Invalid JSON request'); }
}
function json(response, status, value) {
  response.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  response.end(JSON.stringify(value));
}
function image(response, capture) {
  // Binary body with bounded metadata header, never a caller-chosen file path.
  response.writeHead(200, { 'Content-Type': capture.metadata.content_type, 'Content-Length': capture.bytes.length, 'X-Aether-Metadata': Buffer.from(JSON.stringify(capture.metadata)).toString('base64url'), 'Cache-Control': 'no-store' });
  response.end(capture.bytes);
}

export async function startServer({ socketPath = '/aether-control/browser.sock', creationKey = process.env.AETHER_BROWSER_CREATION_KEY || '', launch = () => BrowserSession.launch() } = {}) {
  requireValue(creationKey.length > 0, 'Companion creation identity is required');
  await mkdir(process.env.HOME || '/tmp/aether-browser-home', { recursive: true, mode: 0o700 });
  let startupError;
  let session;
  const startup = launch().then(async (value) => { session = value; await value.initialize(); return value; }).catch((error) => { startupError = error; console.error(`Browser sandbox startup failed: ${error.message}`); });
  let queue = Promise.resolve();
  let pending = 0;
  const serialize = (operation, signal) => {
    requireValue(pending < 16, 'Companion operation queue is full', 'resource_limit');
    pending++;
    const next = queue.then(() => { signal.throwIfAborted(); return operation(); });
    queue = next.catch(() => {}).finally(() => { pending--; });
    return next;
  };
  const server = http.createServer({ maxHeaderSize: 8192, requestTimeout: 15000, headersTimeout: 10000 }, async (request, response) => {
    const controller = new AbortController();
    request.once('aborted', () => controller.abort(new BrowserError('cancelled', 'Request cancelled')));
    response.once('close', () => { if (!response.writableEnded) controller.abort(new BrowserError('cancelled', 'Request cancelled')); });
    try {
      requireValue(!request.headers.origin, 'Browser-origin requests are not accepted');
      if (request.method === 'GET' && request.url === '/health') {
        if (startupError) return json(response, 503, { error: { code: 'unavailable', message: startupError.message.slice(0, 8192) }, creation_key: creationKey });
        if (!session?.context || !session.browser.isConnected()) return json(response, 503, { error: { code: 'unavailable', message: 'Chromium is not connected' }, creation_key: creationKey });
        return json(response, 200, { creation_key: creationKey, session_id: session.sessionID, protocol_version: 1 });
      }
      requireValue(request.method === 'POST', 'Only POST operations are accepted');
      const body = await readJSON(request);
      await startup;
      if (startupError) throw new BrowserError('unavailable', startupError.message.slice(0, 8192));
      requireValue(session.browser.isConnected(), 'Chromium exited; explicit companion restart required', 'unavailable');
      controller.signal.throwIfAborted();
      switch (request.url) {
        case '/command': {
          const operation = () => session.execute(body, controller.signal);
          const result = body.operation === 'wait' ? await operation() : await serialize(operation, controller.signal);
          if (!controller.signal.aborted) json(response, 200, result);
          break;
        }
        case '/capture': image(response, await serialize(() => session.capture(body), controller.signal)); break;
        case '/terminal': image(response, await serialize(() => renderTerminal(session.browser, body), controller.signal)); break;
        case '/stream': await serialize(() => streamPage(session, body, response), controller.signal); break;
        default: throw new BrowserError('invalid_request', 'Unknown companion endpoint');
      }
    } catch (error) {
      if (controller.signal.aborted) return;
      if (response.headersSent) { response.destroy(error); return; }
      const code = error.code || (error.name === 'TimeoutError' ? 'timeout' : 'browser_error');
      json(response, code === 'resource_limit' ? 429 : code === 'unavailable' ? 503 : 400, { error: { code, message: String(error.message).slice(0, 8192) } });
    }
  });
  server.maxConnections = 24;
  // Only the host and this companion can reach the private bind directory.
  try {
    const info = await lstat(socketPath);
    requireValue(info.isSocket(), 'Refusing to replace a non-socket control path');
    await unlink(socketPath);
  } catch (error) { if (error.code !== 'ENOENT') throw error; }
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(socketPath, resolve); });
  await chmod(socketPath, 0o600);
  return {
    server,
    ready: startup,
    close: async () => {
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
      await startup;
      if (session) await session.close();
      await unlink(socketPath).catch((error) => { if (error.code !== 'ENOENT') throw error; });
    },
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const companion = await startServer();
  let closing = false;
  for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => {
    if (closing) return;
    closing = true;
    const deadline = setTimeout(() => process.exit(1), 10000);
    void companion.close().then(() => { clearTimeout(deadline); process.exit(0); }, (error) => { console.error(error); process.exit(1); });
  });
}
