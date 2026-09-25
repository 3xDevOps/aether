import { randomUUID } from 'node:crypto';
import { chromium } from 'playwright';

export const limits = Object.freeze({ request: 2 * 1024 * 1024, image: 8 * 1024 * 1024, frame: 2 * 1024 * 1024, pages: 16, nodes: 500, chars: 32000, logs: 100, timeout: 10000 });
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
const delay = (ms, signal) => new Promise((resolve, reject) => {
  const abort = () => { clearTimeout(timer); reject(signal.reason); };
  const timer = setTimeout(() => { signal?.removeEventListener('abort', abort); resolve(); }, ms);
  if (signal?.aborted) abort(); else signal?.addEventListener('abort', abort, { once: true });
});

export class BrowserSession {
  static async launch() {
    // Playwright's transport is --remote-debugging-pipe. Sandbox failure is fatal;
    // there is deliberately no retry with --no-sandbox or a debugging listener.
    const browser = await chromium.launch({ headless: true, chromiumSandbox: true, args: ['--disable-dev-shm-usage=false'] });
    return new BrowserSession(browser);
  }
  constructor(browser) {
    this.browser = browser;
    this.pages = new Map();
    this.selected = '';
    this.sessionID = randomUUID();
    this.logSequence = 0;
    this.context = null;
    this.resetting = null;
    this.closed = false;
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
    const state = { page, id: randomUUID(), revision: 1, viewportID: randomUUID(), viewportChanged: Date.now() / 1000, nodes: new Map(), console: [], network: [], streams: new Set(), cdp: null, touches: new Map() };
    this.pages.set(state.id, state);
    this.selected ||= state.id;
    const log = (kind, value) => {
      const entries = state[kind];
      entries.push({ sequence: ++this.logSequence, captured_at: new Date().toISOString(), ...value });
      if (entries.length > limits.logs) entries.shift();
    };
    page.on('console', (message) => { if (['error', 'warning'].includes(message.type())) log('console', { level: message.type(), text: short(message.text()) }); });
    page.on('pageerror', (error) => log('console', { level: 'error', text: short(error.message) }));
    page.on('requestfailed', (request) => log('network', { url: short(request.url()), method: request.method(), text: short(request.failure()?.errorText) }));
    page.on('response', (response) => { if (response.status() >= 400) log('network', { url: short(response.url()), status: response.status(), method: response.request().method() }); });
    page.on('framenavigated', (frame) => {
      // Child-frame navigations also invalidate the composite DOM observation.
      state.revision++;
      state.viewportID = randomUUID();
      state.viewportChanged = Date.now() / 1000;
      state.touches.clear();
      void this.clearNodes(state);
    });
    page.on('close', () => {
      void this.clearNodes(state);
      for (const stream of state.streams) stream.end();
      this.pages.delete(state.id);
      if (this.selected === state.id) this.selected = this.pages.keys().next().value ?? '';
    });
    page.on('dialog', (dialog) => { log('console', { level: 'warning', text: `Dismissed ${dialog.type()} dialog: ${short(dialog.message())}` }); void dialog.dismiss().catch(() => {}); });
    return state;
  }
  async clearNodes(state) {
    const handles = [...state.nodes.values()];
    state.nodes.clear();
    await Promise.all(handles.map((handle) => handle.dispose().catch(() => {})));
  }
  target(request) {
    requireValue(request.session_id === this.sessionID, 'Browser session changed; list pages again', 'stale_target');
    const state = this.pages.get(request.page_id);
    requireValue(state && !state.page.isClosed(), 'Page closed or unknown; list pages again', 'stale_target');
    requireValue(request.page_revision === state.revision, 'Page navigated; take another snapshot', 'stale_target');
    return state;
  }
  async node(state, request) {
    const node = state.nodes.get(request.node_id);
    requireValue(node, 'Node reference expired; take another snapshot', 'stale_target');
    let connected = false;
    try { connected = await node.evaluate((element) => element.isConnected); } catch { /* navigation destroys handles */ }
    requireValue(connected && state.revision === request.page_revision, 'Node detached; take another snapshot', 'stale_target');
    return node;
  }
  viewport(state, request) {
    requireValue(request.viewport_id === state.viewportID, 'Viewport changed; use a current frame', 'stale_viewport');
    const size = state.page.viewportSize();
    requireValue(Number.isFinite(request.x) && Number.isFinite(request.y) && request.x >= 0 && request.y >= 0 && request.x < size.width && request.y < size.height, 'Input coordinates outside viewport');
  }
  async describe(state) {
    return { session_id: this.sessionID, page_id: state.id, page_revision: state.revision, viewport_id: state.viewportID, url: short(state.page.url()), title: short(await state.page.title().catch(() => '')), ...state.page.viewportSize() };
  }
  async list() {
    return { session_id: this.sessionID, selected_page_id: this.selected, pages: await Promise.all([...this.pages.values()].map((state) => this.describe(state))) };
  }
  async snapshot(state, request) {
    await this.clearNodes(state);
    const maxNodes = boundedInteger(request.max_nodes, 200, 1, limits.nodes);
    const maxChars = boundedInteger(request.max_chars, 16000, 1, limits.chars);
    let remaining = maxChars;
    const nodes = [];
    let truncated = false;
    const revision = state.revision;
    for (const frame of state.page.frames()) {
      if (nodes.length >= maxNodes || remaining <= 0) { truncated = true; break; }
      const collection = await frame.evaluateHandle(({ count }) => {
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
        return found.slice(0, count + 1);
      }, { count: maxNodes - nodes.length });
      const properties = await collection.getProperties();
      await collection.dispose();
      for (const handle of properties.values()) {
        const node = handle.asElement();
        if (!node || nodes.length >= maxNodes || remaining <= 0) { truncated = true; await handle.dispose(); continue; }
        const info = await node.evaluate((element) => {
          const tag = element.tagName.toLowerCase();
          const implicit = { a: element.hasAttribute('href') ? 'link' : '', button: 'button', textarea: 'textbox', select: 'combobox', img: 'img', h1: 'heading', h2: 'heading', h3: 'heading', input: ['checkbox', 'radio'].includes(element.type) ? element.type : element.type === 'button' || element.type === 'submit' ? 'button' : 'textbox' };
          const labelled = (element.getAttribute('aria-labelledby') ?? '').split(/\s+/).slice(0, 20).map((id) => document.getElementById(id)?.textContent ?? '').join(' ').trim();
          const labels = element.labels ? [...element.labels].map((label) => label.textContent).join(' ') : '';
          const ownText = [...element.childNodes].filter((child) => child.nodeType === Node.TEXT_NODE).map((child) => child.textContent).join(' ').trim();
          const name = element.getAttribute('aria-label') || labelled || labels || element.getAttribute('alt') || element.getAttribute('title') || (['button', 'a', 'option'].includes(tag) ? element.textContent : ownText);
          return { tag, role: element.getAttribute('role') || implicit[tag] || '', name: (name ?? '').slice(0, 1024), text: ownText.slice(0, 1024), value: element.type === 'password' ? undefined : typeof element.value === 'string' ? element.value.slice(0, 1024) : undefined, disabled: !!element.disabled, checked: typeof element.checked === 'boolean' ? element.checked : undefined, expanded: element.getAttribute('aria-expanded') ?? undefined };
        });
        const nodeID = randomUUID();
        for (const key of ['name', 'text', 'value']) {
          if (typeof info[key] === 'string') { info[key] = info[key].slice(0, remaining); remaining -= info[key].length; }
        }
        state.nodes.set(nodeID, node);
        nodes.push({ node_id: nodeID, frame_url: short(frame.url(), 512), ...info });
      }
    }
    requireValue(revision === state.revision, 'Page navigated during snapshot; retry', 'stale_target');
    return { page: await this.describe(state), snapshot: { nodes, truncated } };
  }
  async execute(request, signal) {
    signal?.throwIfAborted();
    requireValue(request && typeof request === 'object', 'Request must be an object');
    await this.initialize();
    const timeout = boundedInteger(request.timeout_ms, 5000, 1, limits.timeout);
    if (request.operation === 'pages') return this.list();
    if (request.operation === 'reset') {
      requireValue(request.session_id === this.sessionID, 'Browser session changed', 'stale_target');
      const old = this.context;
      this.context = null;
      await old.close();
      this.pages.clear();
      this.selected = '';
      this.sessionID = randomUUID();
      await this.initialize();
      return this.list();
    }
    if (request.operation === 'open') {
      requireValue(!request.session_id || request.session_id === this.sessionID, 'Browser session changed', 'stale_target');
      requireValue(this.pages.size < limits.pages, 'Maximum open pages reached', 'resource_limit');
      const url = this.url(request.url || 'about:blank');
      const page = await this.context.newPage();
      const state = this.register(page);
      this.selected = state.id;
      if (url !== 'about:blank') await page.goto(url, { timeout, waitUntil: 'domcontentloaded' });
      return { page: await this.describe(state) };
    }
    const state = this.target(request);
    const page = state.page;
    const options = { timeout };
    switch (request.operation) {
      case 'select': this.selected = state.id; await page.bringToFront(); break;
      case 'close': await page.close(); return this.list();
      case 'navigate': await page.goto(this.url(request.url), { ...options, waitUntil: 'domcontentloaded' }); break;
      case 'back': await page.goBack({ ...options, waitUntil: 'domcontentloaded' }); break;
      case 'forward': await page.goForward({ ...options, waitUntil: 'domcontentloaded' }); break;
      case 'reload': await page.reload({ ...options, waitUntil: 'domcontentloaded' }); break;
      case 'snapshot': return this.snapshot(state, request);
      case 'click': await (await this.node(state, request)).click({ ...options, button: request.button || 'left' }); break;
      case 'fill': requireValue(typeof request.text === 'string' && request.text.length <= limits.chars, 'Text exceeds limit'); await (await this.node(state, request)).fill(request.text, options); break;
      case 'select_option': requireValue(Array.isArray(request.values) && request.values.length <= 100 && request.values.every((value) => typeof value === 'string' && value.length <= 1024), 'Invalid selection values'); await (await this.node(state, request)).selectOption(request.values, options); break;
      case 'key': requireValue(typeof request.key === 'string' && request.key.length <= 100, 'Invalid key'); if (request.node_id) await (await this.node(state, request)).press(request.key, options); else if (request.action === 'down') await page.keyboard.down(request.key); else if (request.action === 'up') await page.keyboard.up(request.key); else await page.keyboard.press(request.key); break;
      case 'text': requireValue(typeof request.text === 'string' && request.text.length <= limits.chars, 'Text exceeds limit'); await page.keyboard.insertText(request.text); break;
      case 'scroll': this.viewport(state, request); requireValue(Number.isFinite(request.delta_x) && Number.isFinite(request.delta_y) && Math.abs(request.delta_x) <= 10000 && Math.abs(request.delta_y) <= 10000, 'Invalid scroll delta'); await page.mouse.move(request.x, request.y); await page.mouse.wheel(request.delta_x, request.delta_y); break;
      case 'pointer': {
        this.viewport(state, request);
        requireValue(['move', 'down', 'up', 'click'].includes(request.action), 'Invalid pointer action');
        requireValue(!request.button || ['left', 'right', 'middle'].includes(request.button), 'Invalid pointer button');
        await page.mouse.move(request.x, request.y);
        const mouseOptions = { button: request.button || 'left', clickCount: boundedInteger(request.click_count, 1, 1, 3) };
        if (request.action === 'click') await page.mouse.click(request.x, request.y, mouseOptions);
        else if (request.action !== 'move') await page.mouse[request.action](mouseOptions);
        break;
      }
      case 'touch': {
        this.viewport(state, request);
        requireValue(['start', 'move', 'end', 'cancel'].includes(request.action), 'Invalid touch action');
        const id = boundedInteger(request.touch_id, 1, 1, 10);
        if (request.action === 'start' || request.action === 'move') state.touches.set(id, { x: request.x, y: request.y, id });
        else state.touches.delete(id);
        if (request.action === 'cancel') state.touches.clear();
        const cdp = await this.cdp(state);
        await cdp.send('Input.dispatchTouchEvent', { type: { start: 'touchStart', move: 'touchMove', end: 'touchEnd', cancel: 'touchCancel' }[request.action], touchPoints: [...state.touches.values()] });
        break;
      }
      case 'viewport': {
        const width = boundedInteger(request.width, 1280, 240, 2560);
        const height = boundedInteger(request.height, 800, 240, 1600);
        await page.setViewportSize({ width, height });
        state.viewportID = randomUUID();
        state.viewportChanged = Date.now() / 1000;
        break;
      }
      case 'wait': {
        requireValue(['text', 'url', 'visible', 'hidden', 'load'].includes(request.condition), 'Unknown wait condition');
        requireValue(typeof request.text !== 'string' || request.text.length <= 2048, 'Wait text exceeds limit');
        let matched = false;
        const deadline = Date.now() + timeout;
        do {
          signal?.throwIfAborted();
          this.target(request);
          if (request.condition === 'url') matched = page.url().includes(request.text || '');
          else if (request.condition === 'load') matched = await page.evaluate(() => document.readyState === 'complete');
          else if (request.condition === 'text') matched = await page.evaluate((text) => document.body?.innerText.slice(0, 1000000).includes(text) ?? false, request.text || '');
          else {
            const node = await this.node(state, request);
            matched = request.condition === 'visible' ? await node.isVisible() : await node.isHidden();
          }
          if (!matched) await delay(Math.min(100, Math.max(0, deadline - Date.now())), signal);
        } while (!matched && Date.now() < deadline);
        return { page: await this.describe(state), matched, timed_out: !matched };
      }
      case 'console': case 'network': return { page: await this.describe(state), logs: state[request.operation].filter((entry) => entry.sequence > (request.after || 0)), latest_sequence: this.logSequence };
      default: throw new BrowserError('invalid_request', 'Unknown browser operation');
    }
    signal?.throwIfAborted();
    return { page: await this.describe(state) };
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
    const revision = state.revision;
    const bytes = await state.page.screenshot({ type: 'png', timeout: limits.timeout, animations: 'allow' });
    requireValue(bytes.length <= limits.image, 'Screenshot exceeds image limit', 'resource_limit');
    requireValue(revision === state.revision, 'Page navigated during screenshot', 'stale_target');
    return { bytes, metadata: { ...await this.describe(state), captured_at: new Date().toISOString(), content_type: 'image/png' } };
  }
  async close() { this.closed = true; await this.browser.close(); }
}
