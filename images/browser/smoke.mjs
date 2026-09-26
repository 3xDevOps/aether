import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { BrowserSession } from './session.mjs';
import { startServer } from './server.mjs';

// Run as the image's non-root user. No sandbox override is accepted here.
// node /opt/aether-browser/smoke.mjs
export async function smoke() {
  const directory = await mkdtemp(join(tmpdir(), 'aether-browser-smoke-'));
  const socketPath = join(directory, 'control.sock');
  const app = http.createServer((request, response) => {
    response.setHeader('Content-Type', 'text/html');
    if (request.url === '/popup') return response.end('<title>Authentication popup</title><h1>Popup ready</h1>');
    response.end(`<!doctype html><title>Browser smoke app</title><h1>Counter</h1><button id="counter" onclick="this.textContent='Count: 1'">Count: 0</button><button onclick="window.open('/popup')">Popup</button><script>console.error('smoke-console');fetch('/failure').then(()=>{});</script>`);
  });
  await new Promise((resolve, reject) => { app.once('error', reject); app.listen(0, '127.0.0.1', resolve); });
  const origin = `http://127.0.0.1:${app.address().port}`;
  let companion;
  let session;
  const call = (endpoint, value, signal) => new Promise((resolve, reject) => {
    const request = http.request({ socketPath, path: endpoint, method: value === undefined ? 'GET' : 'POST', signal, headers: value === undefined ? {} : { 'Content-Type': 'application/json' } }, (response) => {
      const chunks = [];
      response.on('data', (chunk) => chunks.push(chunk));
      response.on('error', reject);
      response.on('end', () => {
        const bytes = Buffer.concat(chunks);
        if (response.statusCode !== 200) return reject(new Error(bytes.toString()));
        if (response.headers['content-type'] === 'image/png') return resolve({ bytes, metadata: JSON.parse(Buffer.from(response.headers['x-aether-metadata'], 'base64url')) });
        resolve(JSON.parse(bytes));
      });
    });
    request.setTimeout(endpoint === '/terminal' ? 35000 : 20000, () => request.destroy(new Error('Smoke request timed out')));
    request.on('error', reject);
    request.end(value === undefined ? undefined : JSON.stringify(value));
  });
  try {
    companion = await startServer({ socketPath, creationKey: 'smoke', launch: async () => { session = await BrowserSession.launch(); return session; } });
    await companion.ready;
    const health = await call('/health');
    assert.equal(health.creation_key, 'smoke');
    let { page } = await call('/command', { operation: 'open', url: origin });
    const snapshot = await call('/command', { ...page, operation: 'snapshot' });
    const counter = snapshot.snapshot.nodes.find((node) => node.role === 'button' && node.name === 'Count: 0');
    assert.ok(counter, 'counter is present in the actual DOM snapshot');
    // Check kernel confinement, not requested flags or diagnostic-page wording.
    const cdp = await session.browser.newBrowserCDPSession();
    const { processInfo } = await cdp.send('SystemInfo.getProcessInfo');
    await cdp.detach();
    const statuses = [];
    for (const process of processInfo.filter(({ type }) => type === 'browser' || type === 'renderer')) {
      const text = await readFile(`/proc/${process.id}/status`, 'utf8');
      const fields = Object.fromEntries(text.trim().split('\n').map((line) => line.split(/:\s*/, 2)));
      statuses.push({ type: process.type, namespaces: fields.NSpid.split(/\s+/).length, filters: Number(fields.Seccomp_filters), seccomp: fields.Seccomp, noNewPrivileges: fields.NoNewPrivs });
    }
    const browserStatus = statuses.find(({ type }) => type === 'browser');
    const renderers = statuses.filter(({ type }) => type === 'renderer');
    assert.ok(browserStatus && renderers.length > 0, 'browser and live renderer kernel state is available');
    for (const renderer of renderers) {
      assert.ok(renderer.namespaces > browserStatus.namespaces, 'renderer has its own nested PID namespace');
      assert.equal(renderer.noNewPrivileges, '1');
      assert.equal(renderer.seccomp, '2');
      assert.ok(renderer.filters > browserStatus.filters, 'renderer installs Chromium seccomp in addition to Docker seccomp');
    }
    ({ page } = await call('/command', { ...page, operation: 'click', node_id: counter.node_id }));
    const waited = await call('/command', { ...page, operation: 'wait', condition: 'text', text: 'Count: 1' });
    assert.equal(waited.matched, true);
    const capture = await call('/capture', page);
    assert.equal(capture.bytes.subarray(0, 8).toString('hex'), '89504e470d0a1a0a');
    assert.equal(capture.metadata.page_id, page.page_id);
    const frame = await new Promise((resolve, reject) => {
      const stream = http.request({ socketPath, path: '/stream', method: 'POST', headers: { 'Content-Type': 'application/json' } }, (response) => {
        let buffered = Buffer.alloc(0);
        response.on('error', reject);
        response.on('data', (chunk) => {
          buffered = Buffer.concat([buffered, chunk]);
          try {
            assert.equal(response.statusCode, 200);
            assert.ok(buffered.length <= 3 * 1024 * 1024);
            if (buffered.length < 8) return;
            const metadataSize = buffered.readUInt32BE(0);
            const imageSize = buffered.readUInt32BE(4);
            assert.ok(metadataSize > 0 && metadataSize <= 16384 && imageSize > 0 && imageSize <= 2 * 1024 * 1024);
            if (buffered.length < 8 + metadataSize + imageSize) return;
            const metadata = JSON.parse(buffered.subarray(8, 8 + metadataSize));
            assert.equal(metadata.viewport_id, page.viewport_id);
            assert.equal(metadata.page_id, page.page_id);
            assert.equal(metadata.content_type, 'image/jpeg');
            resolve({ metadata, imageSize });
            response.destroy();
          } catch (error) { reject(error); response.destroy(); }
        });
      });
      stream.on('error', reject);
      stream.setTimeout(10000, () => stream.destroy(new Error('Screencast frame timed out')));
      stream.end(JSON.stringify(page));
    });
    const fresh = await call('/command', { ...page, operation: 'snapshot' });
    const popup = fresh.snapshot.nodes.find((node) => node.role === 'button' && node.name === 'Popup');
    await call('/command', { ...page, operation: 'click', node_id: popup.node_id });
    let pages;
    const popupDeadline = Date.now() + 5000;
    do {
      pages = await call('/command', { operation: 'pages' });
      if (pages.pages.some((entry) => entry.url === `${origin}/popup`)) break;
      await delay(50);
    } while (Date.now() < popupDeadline);
    assert.ok(pages.pages.some((entry) => entry.url === `${origin}/popup`));
    const logs = await call('/command', { ...page, operation: 'console' });
    assert.ok(logs.logs.some((entry) => entry.text === 'smoke-console'));
    const terminalRequest = { session_id: 'terminal-smoke', screen_revision: 7, output_position: 19, captured_at: new Date().toISOString(), cols: 240, rows: 8, vt: '\x1b[?1049h\x1b[2J\x1b[H\x1b[?25l\x1b[32mTerminal smoke Ω\x1b[0m\r\n界 wide text\x1b[1;240H\x1b[48;2;0;255;0m \x1b[0m' };
    const terminal = await call('/terminal', terminalRequest);
    assert.equal(terminal.metadata.active_buffer, 'alternate');
    assert.equal(terminal.metadata.screen_revision, 7);
    assert.equal(terminal.metadata.output_position, 19);
    assert.equal(terminal.bytes.subarray(0, 8).toString('hex'), '89504e470d0a1a0a');
    const rightmostCellVisible = await session.pages.get(page.page_id).page.evaluate(async (png) => {
      const image = new Image();
      image.src = `data:image/png;base64,${png}`;
      await image.decode();
      const canvas = document.createElement('canvas');
      canvas.width = image.naturalWidth;
      canvas.height = image.naturalHeight;
      const context = canvas.getContext('2d');
      context.drawImage(image, 0, 0);
      const start = Math.floor(canvas.width * 3 / 4);
      const pixels = context.getImageData(start, 0, canvas.width - start, canvas.height).data;
      for (let i = 0; i < pixels.length; i += 4) {
        if (pixels[i] === 0 && pixels[i + 1] === 255 && pixels[i + 2] === 0 && pixels[i + 3] === 255) return true;
      }
      return false;
    }, terminal.bytes.toString('base64'));
    assert.ok(rightmostCellVisible, 'wide terminal capture includes its final column, not just the initial renderer viewport');
    const createContext = session.browser.newContext;
    let freeze;
    let freezeFailed;
    const frozen = new Promise((resolve, reject) => { freeze = resolve; freezeFailed = reject; });
    session.browser.newContext = async function (...args) {
      const context = await createContext.apply(this, args);
      context.once('page', (target) => {
        void (async () => {
          const closed = new Promise((resolve) => context.once('close', resolve));
          const cdp = await context.newCDPSession(target);
          await cdp.send('Emulation.setVirtualTimePolicy', { policy: 'pause' });
          freeze({ closed });
        })().catch(freezeFailed);
      });
      return context;
    };
    const controller = new AbortController();
    const watchdog = new AbortController();
    try {
      const interrupted = call('/terminal', terminalRequest, controller.signal).then(
        (value) => ({ value }), (error) => ({ error }),
      );
      const { closed } = await Promise.race([
        frozen,
        delay(10000, undefined, { signal: watchdog.signal }).then(() => { throw new Error('Terminal renderer did not freeze'); }),
      ]);
      controller.abort();
      assert.equal((await interrupted).error?.code, 'ABORT_ERR');
      await Promise.race([
        closed,
        delay(10000, undefined, { signal: watchdog.signal }).then(() => { throw new Error('Cancelled terminal render kept its context alive'); }),
      ]);
    } finally {
      controller.abort();
      watchdog.abort();
      session.browser.newContext = createContext;
    }
    const survivingPage = await call('/command', { ...page, operation: 'snapshot' });
    assert.ok(survivingPage.snapshot.nodes.some((node) => node.role === 'button' && node.name === 'Count: 1'), 'cancelling a terminal render preserves the app and releases the command queue');
    const reset = await call('/command', { operation: 'reset', session_id: health.session_id });
    assert.notEqual(reset.session_id, health.session_id);
    assert.equal(reset.pages.length, 0);
    const after = await call('/health');
    assert.equal(after.process_id, health.process_id);
    assert.equal(after.session_id, reset.session_id);
    console.log(JSON.stringify({ sandbox: 'namespace+seccomp', localhost: origin, click: 'Count: 1', popup: true, browser_png: capture.bytes.length, screencast_jpeg: frame.imageSize, terminal_png: terminal.bytes.length, terminal_buffer: terminal.metadata.active_buffer, terminal_abort: 'context-closed', reset: 'clean-context' }));
  } finally {
    if (companion) await companion.close();
    else if (session) await session.close();
    app.closeAllConnections();
    await new Promise((resolve) => app.close(resolve));
    await rm(directory, { recursive: true, force: true });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await smoke();
