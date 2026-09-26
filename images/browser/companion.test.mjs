import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { BrowserSession, limits } from './session.mjs';
import { encodeFrame } from './stream.mjs';
import { renderTerminal } from './terminal.mjs';

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
