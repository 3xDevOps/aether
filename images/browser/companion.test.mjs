import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { BrowserSession, limits } from './session.mjs';
import { encodeFrame } from './stream.mjs';
import { renderTerminal } from './terminal.mjs';
import { startServer } from './server.mjs';

// Without Chromium: node --test --test-name-pattern='^pure boundaries:' companion.test.mjs
// The complete suite intentionally requires a working non-root Chromium sandbox.
test('pure boundaries: transport and terminal reject oversized or unsupported captures', async () => {
  assert.throws(() => encodeFrame({}, Buffer.alloc(limits.frame + 1)), { code: 'resource_limit' });
  assert.throws(() => encodeFrame({ title: 'x'.repeat(16385) }, Buffer.from('image')), { code: 'resource_limit' });
  const snapshot = { session_id: 'terminal', screen_revision: 1, output_position: 0, captured_at: new Date().toISOString(), cols: 80, rows: 24, vt: '\x1b_Ga=T;payload\x1b\\' };
  await assert.rejects(renderTerminal(null, snapshot), { code: 'unsupported_graphics' });
  await assert.rejects(renderTerminal(null, { ...snapshot, vt: '', cols: 321 }), { code: 'invalid_request' });
  await assert.rejects(renderTerminal(null, { ...snapshot, vt: '', output_position: 2 ** 53 }), { code: 'invalid_request' });
});

test('detached DOM targets, navigation, popups and reset preserve session boundaries', { timeout: 120000 }, async (t) => {
  const app = http.createServer((request, response) => {
    if (request.url === '/failure') { response.writeHead(503); response.end('failure'); return; }
    response.setHeader('Content-Type', 'text/html');
    response.end('<!doctype html><title>Boundary fixture</title><button id="target" onclick="window.clicks=(window.clicks||0)+1">Target</button><input id="value" aria-label="Value"><a href="/popup" target="_blank">Popup</a>');
  });
  await new Promise((resolve, reject) => { app.once('error', reject); app.listen(0, '127.0.0.1', resolve); });
  t.after(async () => { app.closeAllConnections(); await new Promise((resolve) => app.close(resolve)); });
  const session = await BrowserSession.launch();
  t.after(() => session.close());
  const origin = `http://127.0.0.1:${app.address().port}`;
  let { page } = await session.execute({ operation: 'open', url: origin, width: 800, height: 600 });
  assert.deepEqual([page.width, page.height], [800, 600]);
  const longWait = await session.execute({ ...page, operation: 'wait', condition: 'url', text: origin, timeout_ms: 30000 });
  assert.equal(longWait.matched, true);
  const state = session.pages.get(page.page_id);
  const first = await session.execute({ ...page, operation: 'snapshot' });
  const target = first.snapshot.nodes.find((node) => node.name === 'Target' && node.role === 'button');
  assert.ok(target);
  await state.page.evaluate(() => document.getElementById('target').replaceWith(document.getElementById('target').cloneNode(true)));
  await assert.rejects(session.execute({ ...page, operation: 'click', node_id: target.node_id }), { code: 'stale_target' });
  assert.equal(await state.page.evaluate(() => window.clicks || 0), 0, 'replacement must not receive the stale action');
  const second = await session.execute({ ...page, operation: 'snapshot' });
  const current = second.snapshot.nodes.find((node) => node.name === 'Target' && node.role === 'button');
  await session.execute({ ...page, operation: 'click', node_id: current.node_id });
  assert.equal(await state.page.evaluate(() => window.clicks), 1);
  const input = second.snapshot.nodes.find((node) => node.role === 'textbox' && node.name === 'Value');
  await session.execute({ ...page, operation: 'fill', node_id: input.node_id, text: 'typed' });
  await session.execute({ ...page, operation: 'fill', node_id: input.node_id, text: '' });
  assert.equal(await state.page.locator('#value').inputValue(), '');
  await state.page.evaluate(() => {
    document.getElementById('target').addEventListener('click', (event) => { window.shiftClicked = event.shiftKey; });
    document.getElementById('value').addEventListener('keydown', (event) => { window.controlPressed = event.ctrlKey; });
  });
  await session.execute({ ...page, operation: 'click', node_id: current.node_id, modifiers: ['Shift'] });
  assert.equal(await state.page.evaluate(() => window.shiftClicked), true);
  await session.execute({ ...page, operation: 'key', node_id: input.node_id, key: 'a', modifiers: ['Control'] });
  assert.equal(await state.page.evaluate(() => window.controlPressed), true);
  await assert.rejects(session.execute({ ...page, operation: 'pointer', action: 'click', x: 1, y: 1, modifiers: ['Shift'] }), { code: 'invalid_request' });
  await state.page.evaluate(() => { document.body.style.height = '2000px'; });
  const full = await session.capture({ ...page, full_page: true });
  assert.ok(full.metadata.height >= 2000 && full.metadata.height > page.height);
  assert.equal(full.bytes.readUInt32BE(20), full.metadata.height);
  assert.equal(full.metadata.viewport_id, '', 'document-coordinate evidence is not a viewport input frame');
  await state.page.evaluate(() => { document.body.style.height = '9000px'; });
  await assert.rejects(session.capture({ ...page, full_page: true }), { code: 'resource_limit' });
  await state.page.evaluate(() => { document.body.style.height = ''; });
  const oldViewport = page.viewport_id;
  ({ page } = await session.execute({ ...page, operation: 'viewport', width: 640, height: 480 }));
  await assert.rejects(session.execute({ ...page, operation: 'pointer', viewport_id: oldViewport, action: 'click', x: 1, y: 1 }), { code: 'stale_viewport' });
  const oldPage = page;
  ({ page } = await session.execute({ ...page, operation: 'navigate', url: `${origin}/next` }));
  await assert.rejects(session.execute({ ...oldPage, operation: 'click', node_id: current.node_id }), { code: 'stale_target' });
  const popupReady = state.page.waitForEvent('popup');
  await state.page.getByText('Popup', { exact: true }).click();
  const popup = await popupReady;
  await popup.waitForLoadState('domcontentloaded');
  const pages = await session.execute({ operation: 'pages' });
  assert.ok(pages.pages.some((entry) => entry.url === `${origin}/popup`));
  await state.page.evaluate(async () => {
    localStorage.setItem('session-secret', 'present');
    document.cookie = 'session-cookie=present';
    for (let index = 0; index < 110; index++) console.error(`error-${index}`);
    await fetch('/failure');
  });
  const logs = await session.execute({ ...page, operation: 'console', after: 0 });
  assert.equal(logs.truncated, true);
  assert.equal(logs.logs.length, limits.logs);
  assert.ok(logs.logs.some((entry) => entry.text === 'error-109'));
  const network = await session.execute({ ...page, operation: 'network', after: 0 });
  assert.ok(network.logs.some((entry) => entry.status === 503));
  const process = session.processID;
  const reset = await session.execute({ operation: 'reset', session_id: page.session_id });
  assert.equal(reset.pages.length, 0);
  assert.equal(session.processID, process);
  await assert.rejects(session.execute({ ...page, operation: 'snapshot' }), { code: 'stale_target' });
  ({ page } = await session.execute({ operation: 'open', session_id: reset.session_id, url: origin }));
  const clean = session.pages.get(page.page_id).page;
  assert.deepEqual(await clean.evaluate(() => [localStorage.getItem('session-secret'), document.cookie]), [null, '']);
  await clean.evaluate(() => {
    const hidden = document.createElement('div');
    hidden.style.display = 'none';
    hidden.innerHTML = '<span>hidden</span>'.repeat(10001);
    document.body.prepend(hidden);
  });
  const bounded = await session.execute({ ...page, operation: 'snapshot' });
  assert.equal(bounded.snapshot.truncated, true, 'DOM scan cap must not claim a complete observation');
});

test('physical input keeps live identity and the last observed title until observation refreshes it', { timeout: 120000 }, async (t) => {
  const fixture = await liveCompanion(t, '<!doctype html><title>Observed title</title><button style="position:absolute;left:20px;top:10px;width:120px;height:40px" onpointerdown="document.title=\'Pressed title\';window.presses=(window.presses||0)+1">Press</button><input aria-label="Value" style="position:absolute;left:20px;top:80px" onkeydown="document.title=\'Typed title\'">');
  const { session, origin, call } = fixture;
  let { page } = await call('/command', { operation: 'open', url: origin, width: 640, height: 480 });
  const state = session.pages.get(page.page_id);
  await state.page.waitForLoadState('load');
  ({ page } = await call('/command', { ...page, operation: 'snapshot' }));
  assert.equal(page.title, 'Observed title');
  const pressed = await call('/command', { ...page, operation: 'pointer', action: 'down', x: 80, y: 25 });
  assert.equal(await state.page.evaluate(() => window.presses), 1);
  assert.equal(await state.page.title(), 'Pressed title');
  assert.deepEqual(pressed.page, page, 'input keeps authoritative geometry and identity without replacing the observed title');
  await call('/command', { ...pressed.page, operation: 'pointer', action: 'up', x: 80, y: 25 });
  ({ page } = await call('/command', { ...page, operation: 'snapshot' }));
  assert.equal(page.title, 'Pressed title');
  await state.page.getByLabel('Value').focus();
  const typed = await call('/command', { ...page, operation: 'key', key: 'K' });
  assert.equal(await state.page.getByLabel('Value').inputValue(), 'K');
  assert.equal(await state.page.title(), 'Typed title');
  assert.deepEqual(typed.page, page);
  ({ page } = await call('/command', { ...page, operation: 'snapshot' }));
  assert.equal(page.title, 'Typed title');
  const navigated = await call('/command', { ...page, operation: 'navigate', url: `${origin}/next` });
  assert.equal(navigated.page.url, `${origin}/next`);
  assert.notEqual(navigated.page.page_revision, page.page_revision);
  assert.equal(navigated.page.title, 'Observed title', 'navigation must not retain the previous document title');
});

async function liveCompanion(t, html) {
  const app = http.createServer((request, response) => {
    response.setHeader('Content-Type', 'text/html');
    response.end(html);
  });
  await new Promise((resolve, reject) => { app.once('error', reject); app.listen(0, '127.0.0.1', resolve); });
  t.after(async () => { app.closeAllConnections(); await new Promise((resolve) => app.close(resolve)); });
  const directory = await mkdtemp(join(tmpdir(), 'aether-browser-test-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const socketPath = join(directory, 'browser.sock');
  let session;
  const companion = await startServer({ socketPath, creationKey: 'companion-test', launch: async () => { session = await BrowserSession.launch(); return session; } });
  t.after(() => companion.close());
  await companion.ready;
  const call = (path, value) => new Promise((resolve, reject) => {
    const request = http.request({ socketPath, path, method: 'POST' }, (response) => {
      const chunks = [];
      response.on('data', (chunk) => chunks.push(chunk));
      response.on('error', reject);
      response.on('end', () => {
        const result = JSON.parse(Buffer.concat(chunks));
        if (response.statusCode !== 200) reject(Object.assign(new Error(result.error.message), result.error));
        else resolve(result);
      });
    });
    request.on('error', reject);
    request.setTimeout(15000, () => request.destroy(new Error('Companion request timed out')));
    request.end(JSON.stringify(value));
  });
  const watch = (page) => {
    const records = [];
    let stream;
    const first = new Promise((resolve, reject) => {
      stream = http.request({ socketPath, path: '/stream', method: 'POST' }, (response) => {
        if (response.statusCode !== 200) { reject(new Error(`Stream HTTP ${response.statusCode}`)); response.resume(); return; }
        let buffered = Buffer.alloc(0);
        response.on('error', reject);
        response.on('data', (chunk) => {
          buffered = Buffer.concat([buffered, chunk]);
          while (buffered.length >= 8) {
            const metadataSize = buffered.readUInt32BE(0);
            const imageSize = buffered.readUInt32BE(4);
            if (buffered.length < 8 + metadataSize + imageSize) break;
            const record = { metadata: JSON.parse(buffered.subarray(8, 8 + metadataSize)), bytes: buffered.subarray(8 + metadataSize, 8 + metadataSize + imageSize) };
            buffered = buffered.subarray(8 + metadataSize + imageSize);
            records.push(record);
            if (records.length > 32) records.shift();
            resolve(record);
          }
        });
      });
      stream.on('error', reject);
      stream.setTimeout(10000, () => stream.destroy(new Error('Current frame timed out')));
      stream.end(JSON.stringify(page));
    });
    t.after(() => stream.destroy());
    return { first, records, close: () => stream.destroy() };
  };
  return { session, call, watch, origin: `http://127.0.0.1:${app.address().port}` };
}

test('late static viewers receive original current pixels and invalidated frames never cross page geometry', { timeout: 120000 }, async (t) => {
  const { session, call, watch, origin } = await liveCompanion(t, '<!doctype html><title>Static observers</title><style>body{background:#14532d;color:white}</style><h1>Unchanged current content</h1><script>window.boot=crypto.randomUUID()</script>');
  let { page } = await call('/command', { operation: 'open', url: origin, width: 800, height: 600 });
  const native = session.pages.get(page.page_id).page;
  const boot = await native.evaluate(() => window.boot);
  const first = watch(page);
  await first.first;
  await delay(250); // Let the initial compositor updates settle, without changing the app.
  const second = watch(page);
  const replay = await second.first;
  let original;
  const deadline = Date.now() + 2000;
  do {
    original = first.records.find((record) => record.metadata.sequence === replay.metadata.sequence);
    if (!original) await delay(10);
  } while (!original && Date.now() < deadline);
  assert.ok(original, 'late viewer receives a frame already observed by the first viewer');
  assert.deepEqual(replay, original, 'pixels, actual timestamp, revision and viewport must remain unchanged');
  assert.equal(replay.metadata.viewport_id, page.viewport_id);
  assert.equal(await native.evaluate(() => window.boot), boot, 'attaching must not reload the app');
  second.close();
  ({ page } = await call('/command', { ...page, operation: 'viewport', width: 640, height: 480 }));
  const resized = watch(page);
  const resizedFrame = await resized.first;
  assert.equal(resizedFrame.metadata.viewport_id, page.viewport_id);
  assert.deepEqual([resizedFrame.metadata.width, resizedFrame.metadata.height], [640, 480]);
  assert.ok(resizedFrame.metadata.sequence > replay.metadata.sequence);
  resized.close();
  ({ page } = await call('/command', { ...page, operation: 'navigate', url: `${origin}/next` }));
  const navigated = watch(page);
  const nextFrame = await navigated.first;
  assert.equal(nextFrame.metadata.page_revision, page.page_revision);
  assert.equal(nextFrame.metadata.viewport_id, page.viewport_id);
  assert.ok(nextFrame.metadata.sequence > resizedFrame.metadata.sequence);
  first.close();
  navigated.close();
  await delay(100);
  const reattached = await watch(page).first;
  assert.ok(reattached.metadata.sequence > nextFrame.metadata.sequence, 'stream restart cannot reuse its old cache');
});

test('host authority cleanup releases native held input without clearing another session or app state', { timeout: 120000 }, async (t) => {
  const { session, call, origin } = await liveCompanion(t, `<!doctype html><title>Native input</title><style>body{height:500px;touch-action:none}input{width:150px}</style><input id="value"><script>
    window.events=[];
    for(const type of ['keydown','keyup','mousemove','mouseup','touchstart','touchmove','touchend','touchcancel'])
      document.addEventListener(type,e=>events.push({type,key:e.key,ctrl:e.ctrlKey,shift:e.shiftKey,repeat:e.repeat,buttons:e.buttons,touches:e.touches?.length}));
    document.cookie='held-test=present';localStorage.setItem('held-test','present');
  </script>`);
  const { page } = await call('/command', { operation: 'open', url: origin, width: 800, height: 600 });
  const native = session.pages.get(page.page_id).page;
  const input = (value) => call('/command', { ...page, ...value });
  await native.locator('#value').focus();
  await input({ operation: 'key', action: 'down', key: 'Control' });
  await input({ operation: 'key', action: 'down', key: 'Shift' });
  await input({ operation: 'key', action: 'down', key: 'ArrowLeft' });
  await input({ operation: 'pointer', action: 'down', button: 'left', x: 250, y: 180 });
  const ignored = await call('/release-input', { session_id: 'not-the-current-session' });
  assert.equal(ignored.released, false);
  await native.locator('#value').focus();
  await input({ operation: 'key', key: 'a' });
  const deniedKey = await native.evaluate(() => events.findLast((event) => event.type === 'keydown'));
  assert.equal(deniedKey.ctrl, true);
  assert.equal(deniedKey.shift, true);
  await input({ operation: 'pointer', action: 'move', x: 260, y: 180 });
  assert.equal(await native.evaluate(() => events.findLast((event) => event.type === 'mousemove').buttons), 1);
  await input({ operation: 'touch', action: 'start', touch_id: 1, x: 300, y: 250 });
  await input({ operation: 'touch', action: 'start', touch_id: 2, x: 350, y: 250 });
  await call('/release-input', { session_id: 'not-the-current-session' });
  await input({ operation: 'touch', action: 'move', touch_id: 1, x: 330, y: 250 });
  assert.equal(await native.evaluate(() => events.findLast((event) => event.type === 'touchmove').touches), 2);
  await assert.rejects(input({ operation: 'release_input' }), { code: 'invalid_request' });
  const released = await call('/release-input', { session_id: page.session_id });
  assert.equal(released.released, true);
  assert.equal(released.session_id, page.session_id);
  assert.equal(await native.evaluate(() => events.findLast((event) => event.type === 'touchcancel').touches), 0);
  await native.locator('#value').focus();
  await input({ operation: 'key', key: 'x' });
  assert.equal(await native.locator('#value').inputValue(), 'x');
  const cleanKey = await native.evaluate(() => events.findLast((event) => event.type === 'keydown'));
  assert.deepEqual([cleanKey.ctrl, cleanKey.shift, cleanKey.repeat], [false, false, false]);
  await input({ operation: 'key', key: 'ArrowLeft' });
  assert.equal(await native.evaluate(() => events.findLast((event) => event.type === 'keydown').repeat), false);
  await input({ operation: 'pointer', action: 'move', x: 270, y: 190 });
  assert.equal(await native.evaluate(() => events.findLast((event) => event.type === 'mousemove').buttons), 0);
  await input({ operation: 'touch', action: 'start', touch_id: 1, x: 300, y: 250 });
  assert.equal(await native.evaluate(() => events.findLast((event) => event.type === 'touchstart').touches), 1);
  await input({ operation: 'touch', action: 'end', touch_id: 1, x: 300, y: 250 });
  assert.equal(await native.evaluate(() => events.findLast((event) => event.type === 'touchend').touches), 0);
  assert.deepEqual(await native.evaluate(() => [document.cookie, localStorage.getItem('held-test')]), ['held-test=present', 'present']);
});
