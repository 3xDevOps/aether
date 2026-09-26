import { randomUUID } from 'node:crypto';
import { chromium } from 'playwright';

export const limits = Object.freeze({ request: 2 * 1024 * 1024, image: 8 * 1024 * 1024, frame: 2 * 1024 * 1024, pages: 16, nodes: 128, snapshotChars: 8192, chars: 32000, logs: 100, timeout: 10000, operationTimeout: 30000 });
export class BrowserError extends Error {
  constructor(code, message) { super(message); this.code = code; }
}
export function requireValue(condition, message, code = 'invalid_request') {
  if (!condition) throw new BrowserError(code, message);
}
export function boundedInteger(value, fallback, min, max) {
  if (value === undefined || value === 0) return fallback;
  requireValue(Number.isInteger(value) && value >= min && value <= max, `expected integer in ${min}..${max}`);
  return value;
}
const short = (value, max = 2048) => String(value ?? '').slice(0, max);
const hotInputOperations = new Set(['pointer', 'key', 'touch', 'text', 'scroll']);
const pointerPreservingOperations = new Set(['pointer', 'snapshot', 'wait', 'console', 'network']);
const delay = (ms, signal) => new Promise((resolve, reject) => {
  const abort = () => { clearTimeout(timer); reject(signal.reason); };
  const timer = setTimeout(() => { signal?.removeEventListener('abort', abort); resolve(); }, ms);
  if (signal?.aborted) abort(); else signal?.addEventListener('abort', abort, { once: true });
});

// Playwright action timeouts do not cover evaluate/title. Bound read-only CDP
// observations too, including pages whose main thread is stuck in app code.
export async function boundedRead(promise, timeout = limits.timeout) {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new BrowserError('timeout', 'Browser observation timed out')), timeout);
    })]);
  } finally { clearTimeout(timer); }
}

export class BrowserSession {
  static async launch() {
    // Playwright's transport is --remote-debugging-pipe. Sandbox failure is fatal;
    // there is deliberately no retry with --no-sandbox or a debugging listener.
    const browser = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true, ignoreDefaultArgs: ['--disable-dev-shm-usage'], timeout: 15000 });
    return new BrowserSession(browser);
  }
  constructor(browser) {
    this.browser = browser;
    this.pages = new Map();
    this.selected = '';
    this.processID = randomUUID();
    this.sessionID = randomUUID();
    this.logSequence = 0;
    this.context = null;
    this.resetting = false;
  }
  async initialize() {
    if (this.context) return;
    const context = await this.browser.newContext({ viewport: { width: 1280, height: 800 }, hasTouch: true, acceptDownloads: false });
    context.setDefaultTimeout(limits.timeout);
    context.on('page', (page) => this.register(page));
    this.context = context;
  }
  register(page) {
    const known = [...this.pages.values()].find((entry) => entry.page === page);
    if (known) return known;
    if (this.pages.size >= limits.pages) { void page.close().catch(() => {}); return null; }
    const state = { page, id: randomUUID(), revision: 1, viewportID: randomUUID(), viewportChanged: Date.now() / 1000, title: '', titleRead: null, pointerX: null, pointerY: null, nodes: new Map(), console: [], network: [], discarded: { console: 0, network: 0 }, streams: new Set(), latestFrame: null, cdp: null, touches: new Map(), keys: new Set(), buttons: new Set() };
    this.pages.set(state.id, state);
    this.selected ||= state.id;
    const log = (kind, value) => {
      const entries = state[kind];
      entries.push({ sequence: ++this.logSequence, captured_at: new Date().toISOString(), ...value });
      if (entries.length > limits.logs) state.discarded[kind] = entries.shift().sequence;
    };
    page.on('console', (message) => { if (['error', 'warning'].includes(message.type())) log('console', { level: message.type(), text: short(message.text()) }); });
    page.on('pageerror', (error) => log('console', { level: 'error', text: short(error.message) }));
    page.on('requestfailed', (request) => log('network', { url: short(request.url()), method: request.method(), text: short(request.failure()?.errorText) }));
    page.on('response', (response) => { if (response.status() >= 400) log('network', { url: short(response.url()), status: response.status(), method: response.request().method() }); });
    page.on('framenavigated', (frame) => {
      // Child-frame navigations also invalidate the composite DOM observation.
      if (frame === page.mainFrame()) { state.title = ''; state.titleRead = null; }
      state.pointerX = state.pointerY = null;
      state.revision++;
      state.viewportID = randomUUID();
      state.viewportChanged = Date.now() / 1000;
      this.invalidateFrames(state);
      void this.clearNodes(state).catch(() => {});
    });
    page.on('close', () => {
      void this.clearNodes(state).catch(() => {});
      this.invalidateFrames(state);
      for (const stream of state.streams) stream.end();
      this.pages.delete(state.id);
      if (this.selected === state.id) this.selected = this.pages.keys().next().value ?? '';
    });
    page.on('crash', () => {
      state.revision++;
      this.invalidateFrames(state);
      void this.clearNodes(state).catch(() => {});
      for (const stream of state.streams) stream.end();
      log('console', { level: 'error', text: 'Page crashed; explicitly reload or close the page' });
    });
    page.on('dialog', (dialog) => { log('console', { level: 'warning', text: `Dismissed ${dialog.type()} dialog: ${short(dialog.message())}` }); void dialog.dismiss().catch(() => {}); });
    return state;
  }
  invalidateFrames(state) {
    state.latestFrame = null;
    for (const stream of state.streams) stream.invalidate();
  }
  // Host-only authority cleanup, serialized with /command by the companion.
  // A different context has already destroyed the named session's input; never
  // release anything in that replacement context.
  async releaseInput(sessionID) {
    requireValue(typeof sessionID === 'string' && sessionID.length > 0, 'Browser session is required');
    if (sessionID !== this.sessionID) return { session_id: this.sessionID, released: false };
    for (const state of this.pages.values()) {
      if (state.page.isClosed()) continue;
      state.pointerX = state.pointerY = null;
      for (const key of state.keys) {
        await boundedRead(state.page.keyboard.up(key));
        state.keys.delete(key);
      }
      for (const button of state.buttons) {
        await boundedRead(state.page.mouse.up({ button }));
        state.buttons.delete(button);
      }
      if (state.touches.size) {
        const cdp = await this.cdp(state);
        await boundedRead(cdp.send('Input.dispatchTouchEvent', { type: 'touchCancel', touchPoints: [] }));
        state.touches.clear();
      }
    }
    return { session_id: this.sessionID, released: true };
  }
  async clearNodes(state) {
    const handles = [...state.nodes.values()];
    state.nodes.clear();
    await boundedRead(Promise.all(handles.map((handle) => handle.dispose().catch(() => {}))));
  }
  target(request) {
    requireValue(request && typeof request === 'object' && request.session_id === this.sessionID, 'Browser session changed; list pages again', 'stale_target');
    const state = this.pages.get(request.page_id);
    requireValue(state && !state.page.isClosed(), 'Page closed or unknown; list pages again', 'stale_target');
    requireValue(request.page_revision === state.revision, 'Page navigated; take another snapshot', 'stale_target');
    return state;
  }
  async node(state, request) {
    const node = state.nodes.get(request.node_id);
    requireValue(node, 'Node reference expired; take another snapshot', 'stale_target');
    let connected = false;
    try { connected = await boundedRead(node.evaluate((element) => element.isConnected)); }
    catch (error) { if (error.code === 'timeout') throw error; /* navigation destroys handles */ }
    requireValue(connected && state.revision === request.page_revision, 'Node detached; take another snapshot', 'stale_target');
    return node;
  }
  async nodeAction(state, request, action) {
    const node = await this.node(state, request);
    try { return await action(node); }
    catch (error) {
      // A framework can replace the element after the initial connected check.
      // ElementHandle never retargets it; preserve that failure as stale_target.
      let connected = true;
      try { connected = await boundedRead(node.evaluate((element) => element.isConnected), 1000); } catch {}
      requireValue(connected && !state.page.isClosed() && state.revision === request.page_revision && state.nodes.has(request.node_id), 'Node detached or page changed; take another snapshot', 'stale_target');
      throw error;
    }
  }
  viewport(state, request) {
    requireValue(request.viewport_id === state.viewportID, 'Viewport changed; use a current frame', 'stale_viewport');
    const size = state.page.viewportSize();
    requireValue(Number.isFinite(request.x) && Number.isFinite(request.y) && request.x >= 0 && request.y >= 0 && request.x < size.width && request.y < size.height, 'Input coordinates outside viewport');
  }
  metadata(state) {
    return { session_id: this.sessionID, page_id: state.id, page_revision: state.revision, viewport_id: state.viewportID, url: short(state.page.url()), title: state.title, ...state.page.viewportSize() };
  }
  observeTitle(state) {
    if (!state.titleRead) {
      const revision = state.revision;
      const pending = state.page.title().then((title) => {
        if (revision === state.revision) state.title = short(title);
      }).catch(() => {}).finally(() => {
        if (state.titleRead === pending) state.titleRead = null;
      });
      state.titleRead = pending;
    }
    return state.titleRead;
  }
  async describe(state) {
    await boundedRead(this.observeTitle(state), 1000).catch(() => {});
    return this.metadata(state);
  }
  async list() {
    // Periodic inventory must not hold the command queue behind a DOM read.
    // At most one read per document remains outstanding, even if it is hung.
    const pages = [...this.pages.values()].map((state) => {
      void this.observeTitle(state);
      return this.metadata(state);
    });
    return { session_id: this.sessionID, selected_page_id: this.selected, pages };
  }
  async snapshot(state, request, signal) {
    const deadline = Date.now() + boundedInteger(request.timeout_ms, 5000, 1, limits.operationTimeout);
    const read = (promise) => boundedRead(promise, Math.max(1, deadline - Date.now()));
    await this.clearNodes(state);
    const maxNodes = boundedInteger(request.max_nodes, limits.nodes, 1, limits.nodes);
    const maxChars = boundedInteger(request.max_chars, limits.snapshotChars, 1, limits.snapshotChars);
    let remaining = maxChars;
    const nodes = [];
    let truncated = false;
    const revision = state.revision;
    const unclaimed = new Set();
    try {
    for (const frame of state.page.frames()) {
      signal?.throwIfAborted();
      requireValue(Date.now() < deadline, 'Snapshot timed out', 'timeout');
      if (nodes.length >= maxNodes || remaining <= 0) { truncated = true; break; }
      const collection = await read(frame.evaluateHandle(({ count }) => {
        const found = [];
        let visited = 0;
        const visit = (root) => {
          const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT);
          for (let element = walker.currentNode; element; element = walker.nextNode()) {
            if (++visited > 10000 || found.length > count) return;
            if (element.nodeType !== Node.ELEMENT_NODE) continue;
            const rect = element.getBoundingClientRect();
            if (rect.width && rect.height && getComputedStyle(element).visibility !== 'hidden') found.push(element);
            if (element.shadowRoot) visit(element.shadowRoot);
          }
        };
        visit(document.documentElement);
        return Object.assign(found.slice(0, count + 1), { scanTruncated: visited > 10000 });
      }, { count: maxNodes - nodes.length }));
      const properties = await read(collection.getProperties());
      void collection.dispose().catch(() => {});
      for (const handle of properties.values()) unclaimed.add(handle);
      for (const [property, handle] of properties) {
        if (property === 'scanTruncated') { truncated ||= await read(handle.jsonValue()); continue; }
        signal?.throwIfAborted();
        requireValue(Date.now() < deadline, 'Snapshot timed out', 'timeout');
        const node = handle.asElement();
        if (!node || nodes.length >= maxNodes || remaining <= 0) { truncated = true; continue; }
        const info = await read(node.evaluate((element) => {
          const tag = element.tagName.toLowerCase();
          const implicit = { a: element.hasAttribute('href') ? 'link' : '', button: 'button', textarea: 'textbox', select: 'combobox', img: 'img', h1: 'heading', h2: 'heading', h3: 'heading', input: ['checkbox', 'radio'].includes(element.type) ? element.type : element.type === 'button' || element.type === 'submit' ? 'button' : 'textbox' };
          const labelled = (element.getAttribute('aria-labelledby') ?? '').split(/\s+/).slice(0, 20).map((id) => document.getElementById(id)?.textContent ?? '').join(' ').trim();
          const labels = element.labels ? [...element.labels].map((label) => label.textContent).join(' ') : '';
          const ownText = [...element.childNodes].filter((child) => child.nodeType === Node.TEXT_NODE).map((child) => child.textContent).join(' ').trim();
          const name = element.getAttribute('aria-label') || labelled || labels || element.getAttribute('alt') || element.getAttribute('title') || (['button', 'a', 'option'].includes(tag) ? element.textContent : ownText);
          return { tag: tag.slice(0, 64), role: (element.getAttribute('role') || implicit[tag] || '').slice(0, 64), name: (name ?? '').slice(0, 1024), text: ownText.slice(0, 1024), value: element.type === 'password' ? undefined : typeof element.value === 'string' ? element.value.slice(0, 1024) : undefined, disabled: !!element.disabled, checked: typeof element.checked === 'boolean' ? element.checked : undefined, expanded: element.getAttribute('aria-expanded')?.slice(0, 16) };
        }));
        const nodeID = randomUUID();
        for (const key of ['name', 'text', 'value']) {
          if (typeof info[key] === 'string') {
            if (info[key].length > remaining) truncated = true;
            info[key] = info[key].slice(0, remaining);
            remaining -= info[key].length;
          }
        }
        state.nodes.set(nodeID, node);
        unclaimed.delete(node);
        nodes.push({ node_id: nodeID, frame_url: short(frame.url(), 512), ...info });
      }
    }
    const page = await this.describe(state);
    requireValue(revision === state.revision, 'Page navigated during snapshot; retry', 'stale_target');
    return { page, snapshot: { nodes, truncated } };
    } catch (error) {
      void this.clearNodes(state).catch(() => {});
      if (revision !== state.revision) throw new BrowserError('stale_target', 'Page navigated during snapshot; retry');
      throw error;
    } finally {
      for (const handle of unclaimed) void handle.dispose().catch(() => {});
    }
  }
  async execute(request, signal) {
    signal?.throwIfAborted();
    requireValue(request && typeof request === 'object' && !Array.isArray(request), 'Request must be an object');
    requireValue(!this.resetting, 'Browser context reset is in progress', 'unavailable');
    const modifiers = request.modifiers === undefined ? [] : request.modifiers;
    requireValue(Array.isArray(modifiers) && modifiers.length <= 5 && new Set(modifiers).size === modifiers.length && modifiers.every((modifier) => ['Alt', 'Control', 'ControlOrMeta', 'Meta', 'Shift'].includes(modifier)), 'Invalid keyboard modifiers');
    requireValue(!modifiers.length || ['click', 'key'].includes(request.operation), 'Modifiers require element click or key press; use separate modifier key down/up events for pointer input');
    requireValue(!modifiers.length || request.operation !== 'key' || !['down', 'up'].includes(request.action), 'Key down/up modifiers must be sent as separate key events');
    await this.initialize();
    const timeout = boundedInteger(request.timeout_ms, 5000, 1, limits.operationTimeout);
    if (request.operation === 'pages') return this.list();
    if (request.operation === 'reset') {
      requireValue(request.session_id === this.sessionID, 'Browser session changed', 'stale_target');
      this.resetting = true;
      try {
        const old = this.context;
        this.context = null;
        await old.close();
        this.pages.clear();
        this.selected = '';
        this.sessionID = randomUUID();
        await this.initialize();
        return this.list();
      } finally { this.resetting = false; }
    }
    if (request.operation === 'open') {
      requireValue(!request.session_id || request.session_id === this.sessionID, 'Browser session changed', 'stale_target');
      requireValue(this.pages.size < limits.pages, 'Maximum open pages reached', 'resource_limit');
      const url = this.url(request.url || 'about:blank');
      const width = boundedInteger(request.width, 1280, 240, 2560);
      const height = boundedInteger(request.height, 800, 240, 1600);
      const page = await this.context.newPage();
      const state = this.register(page);
      requireValue(state, 'Maximum open pages reached while a popup opened', 'resource_limit');
      await page.setViewportSize({ width, height });
      state.viewportID = randomUUID();
      state.viewportChanged = Date.now() / 1000;
      this.selected = state.id;
      if (url !== 'about:blank') await page.goto(url, { timeout, waitUntil: 'domcontentloaded' });
      return { page: await this.describe(state) };
    }
    const state = this.target(request);
    const page = state.page;
    const options = { timeout };
    if (!pointerPreservingOperations.has(request.operation)) state.pointerX = state.pointerY = null;
    switch (request.operation) {
      case 'select': this.selected = state.id; await page.bringToFront(); break;
      case 'close': await page.close(); return this.list();
      case 'navigate': await page.goto(this.url(request.url), { ...options, waitUntil: 'domcontentloaded' }); break;
      case 'back': await page.goBack({ ...options, waitUntil: 'domcontentloaded' }); break;
      case 'forward': await page.goForward({ ...options, waitUntil: 'domcontentloaded' }); break;
      case 'reload': await page.reload({ ...options, waitUntil: 'domcontentloaded' }); break;
      case 'snapshot': return this.snapshot(state, request, signal);
      case 'click': {
        requireValue(!request.button || ['left', 'right', 'middle'].includes(request.button), 'Invalid pointer button');
        const button = request.button || 'left';
        const addedModifiers = modifiers.filter((key) => !state.keys.has(key));
        for (const key of addedModifiers) state.keys.add(key);
        state.buttons.add(button);
        await this.nodeAction(state, request, (node) => node.click({ ...options, button, modifiers: request.modifiers }));
        state.buttons.delete(button);
        for (const key of addedModifiers) state.keys.delete(key);
        break;
      }
      case 'fill': requireValue(typeof request.text === 'string' && request.text.length <= limits.chars, 'Text exceeds limit'); await this.nodeAction(state, request, (node) => node.fill(request.text, options)); break;
      case 'select_option': requireValue(Array.isArray(request.values) && request.values.length <= 100 && request.values.every((value) => typeof value === 'string' && value.length <= 1024), 'Invalid selection values'); await this.nodeAction(state, request, (node) => node.selectOption(request.values, options)); break;
      case 'key': {
        requireValue(typeof request.key === 'string' && request.key.length > 0 && request.key.length <= 100, 'Invalid key');
        requireValue(!request.action || ['down', 'up', 'press'].includes(request.action), 'Invalid key action');
        const key = [...modifiers, request.key].join('+');
        if (request.node_id && ['down', 'up'].includes(request.action)) {
          await this.nodeAction(state, request, (node) => node.focus());
          this.target(request);
        }
        const keys = request.action === 'down' || request.action === 'up' ? [request.key] : key.endsWith('+') ? [...key.slice(0, -1).split('+').filter(Boolean), '+'] : key.split('+');
        requireValue(new Set([...state.keys, ...keys]).size <= 256, 'Maximum held keys reached', 'resource_limit');
        if (request.action === 'up') {
          await page.keyboard.up(request.key);
          state.keys.delete(request.key);
        } else {
          for (const held of keys) state.keys.add(held);
          if (request.action === 'down') await page.keyboard.down(request.key);
          else {
            if (request.node_id) await this.nodeAction(state, request, (node) => node.press(key, options));
            else await page.keyboard.press(key);
            for (const held of keys) state.keys.delete(held);
          }
        }
        break;
      }
      case 'text': requireValue(typeof request.text === 'string' && request.text.length <= limits.chars, 'Text exceeds limit'); await page.keyboard.insertText(request.text); break;
      case 'scroll': this.viewport(state, request); requireValue(Number.isFinite(request.delta_x) && Number.isFinite(request.delta_y) && Math.abs(request.delta_x) <= 10000 && Math.abs(request.delta_y) <= 10000, 'Invalid scroll delta'); await page.mouse.move(request.x, request.y); await page.mouse.wheel(request.delta_x, request.delta_y); break;
      case 'pointer': {
        const positioned = state.pointerX === request.x && state.pointerY === request.y;
        const revision = state.revision;
        // Unknown until the entire operation succeeds. DOM actions and other
        // mutations invalidate this cache rather than guessing their position.
        state.pointerX = state.pointerY = null;
        this.viewport(state, request);
        requireValue(['move', 'down', 'up', 'click'].includes(request.action), 'Invalid pointer action');
        requireValue(!request.button || ['left', 'right', 'middle'].includes(request.button), 'Invalid pointer button');
        if (!positioned || (request.action !== 'down' && request.action !== 'up')) await page.mouse.move(request.x, request.y);
        const mouseOptions = { button: request.button || 'left', clickCount: boundedInteger(request.click_count, 1, 1, 3) };
        if (request.action === 'down' || request.action === 'click') state.buttons.add(mouseOptions.button);
        if (request.action === 'click') await page.mouse.click(request.x, request.y, mouseOptions);
        else if (request.action !== 'move') await page.mouse[request.action](mouseOptions);
        if (request.action === 'up' || request.action === 'click') state.buttons.delete(mouseOptions.button);
        signal?.throwIfAborted();
        if (revision === state.revision) { state.pointerX = request.x; state.pointerY = request.y; }
        break;
      }
      case 'touch': {
        this.viewport(state, request);
        requireValue(['start', 'move', 'end', 'cancel'].includes(request.action), 'Invalid touch action');
        const id = boundedInteger(request.touch_id, 1, 1, 10);
        const cdp = await this.cdp(state);
        if (request.action === 'start' || request.action === 'move') state.touches.set(id, { x: request.x, y: request.y, id });
        const points = request.action === 'cancel' ? [] : [...state.touches.values()].filter((point) => request.action !== 'end' || point.id !== id);
        await cdp.send('Input.dispatchTouchEvent', { type: { start: 'touchStart', move: 'touchMove', end: 'touchEnd', cancel: 'touchCancel' }[request.action], touchPoints: points });
        if (request.action === 'end') state.touches.delete(id);
        if (request.action === 'cancel') state.touches.clear();
        break;
      }
      case 'viewport': {
        const width = boundedInteger(request.width, 1280, 240, 2560);
        const height = boundedInteger(request.height, 800, 240, 1600);
        state.frameBlocked = true;
        this.invalidateFrames(state);
        try { await page.setViewportSize({ width, height }); }
        finally {
          state.viewportID = randomUUID();
          state.viewportChanged = Date.now() / 1000;
          state.frameBlocked = false;
          this.invalidateFrames(state);
        }
        break;
      }
      case 'wait': {
        requireValue(['text', 'url', 'visible', 'hidden', 'load'].includes(request.condition), 'Unknown wait condition');
        if (['text', 'url'].includes(request.condition)) requireValue(typeof request.text === 'string' && request.text.length > 0 && request.text.length <= 2048, 'Wait text must be non-empty and bounded');
        let matched = false;
        const deadline = Date.now() + timeout;
        do {
          signal?.throwIfAborted();
          this.target(request);
          if (request.condition === 'url') matched = page.url().includes(request.text || '');
          else if (request.condition === 'load') matched = await boundedRead(page.evaluate(() => document.readyState === 'complete'), Math.max(1, deadline - Date.now()));
          else if (request.condition === 'text') matched = await boundedRead(page.evaluate((text) => document.body?.innerText.slice(0, 1000000).includes(text) ?? false, request.text || ''), Math.max(1, deadline - Date.now()));
          else {
            const node = await this.node(state, request);
            matched = request.condition === 'visible' ? await node.isVisible() : await node.isHidden();
          }
          if (!matched) await delay(Math.min(100, Math.max(0, deadline - Date.now())), signal);
        } while (!matched && Date.now() < deadline);
        return { page: await this.describe(state), matched, timed_out: !matched };
      }
      case 'console': case 'network': {
        const after = request.after ?? 0;
        requireValue(Number.isSafeInteger(after) && after >= 0 && after <= this.logSequence, 'Invalid log cursor');
        return { page: await this.describe(state), logs: state[request.operation].filter((entry) => entry.sequence > after), latest_sequence: this.logSequence, truncated: after < state.discarded[request.operation] };
      }
      default: throw new BrowserError('invalid_request', 'Unknown browser operation');
    }
    signal?.throwIfAborted();
    // Input acknowledgements must not queue behind a best-effort DOM title
    // read. Identity and geometry remain live; observations refresh the title.
    return { page: hotInputOperations.has(request.operation) ? this.metadata(state) : await this.describe(state) };
  }
  url(value) {
    requireValue(typeof value === 'string' && value.length <= 8192, 'Invalid URL');
    let url;
    try { url = new URL(value); } catch { throw new BrowserError('invalid_request', 'Invalid URL'); }
    requireValue(['http:', 'https:'].includes(url.protocol) || value === 'about:blank', 'Only HTTP(S) and about:blank URLs are supported');
    return url.href;
  }
  async cdp(state) { state.cdp ||= await this.context.newCDPSession(state.page); return state.cdp; }
  async capture(request) {
    const state = this.target(request);
    state.pointerX = state.pointerY = null;
    requireValue(request.full_page === undefined || typeof request.full_page === 'boolean', 'full_page must be a boolean');
    const revision = state.revision;
    const documentSize = () => boundedRead(state.page.evaluate(() => {
      const body = document.body;
      const root = document.documentElement;
      return {
        width: Math.max(body?.scrollWidth ?? 0, body?.offsetWidth ?? 0, root.scrollWidth, root.offsetWidth, root.clientWidth),
        height: Math.max(body?.scrollHeight ?? 0, body?.offsetHeight ?? 0, root.scrollHeight, root.offsetHeight, root.clientHeight),
      };
    }));
    const size = request.full_page ? await documentSize() : state.page.viewportSize();
    requireValue(size.width > 0 && size.height > 0 && size.width <= 8192 && size.height <= 8192 && size.width * size.height <= 32 * 1024 * 1024, 'Full page exceeds screenshot dimension limit', 'resource_limit');
    // Clip to the measured full page so a racing layout cannot allocate an
    // unbounded bitmap. Reject changed geometry instead of returning a crop.
    const bytes = await state.page.screenshot({ type: 'png', timeout: limits.timeout, animations: 'allow', fullPage: !!request.full_page, clip: request.full_page ? { x: 0, y: 0, ...size } : undefined });
    requireValue(bytes.length <= limits.image, 'Screenshot exceeds image limit', 'resource_limit');
    const width = bytes.readUInt32BE(16);
    const height = bytes.readUInt32BE(20);
    requireValue(width === size.width && height === size.height, 'Page geometry changed during screenshot; capture again', 'stale_target');
    if (request.full_page) {
      const after = await documentSize();
      requireValue(after.width === size.width && after.height === size.height, 'Full page geometry changed during screenshot; capture again', 'stale_target');
    }
    const page = await this.describe(state);
    requireValue(revision === state.revision, 'Page navigated during screenshot', 'stale_target');
    return { bytes, metadata: { ...page, viewport_id: request.full_page ? '' : page.viewport_id, width, height, captured_at: new Date().toISOString(), content_type: 'image/png' } };
  }
  async close() { await this.browser.close(); }
}
