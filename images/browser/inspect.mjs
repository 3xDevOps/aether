// The selector, React and redaction logic in describe() is adapted from Orca
// (https://github.com/stablyai/orca, src/main/browser/grab-guest-*.ts at
// 81867c8e), used under the MIT License:
//
// Copyright (c) 2026 Lovecast Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// Both functions below run inside the page, so neither may use this module's scope.
function pick({ x, y }) {
  let element = document.elementFromPoint(x, y);
  for (let depth = 0; element?.shadowRoot && depth < 16; depth++) {
    const inner = element.shadowRoot.elementFromPoint(x, y);
    if (!inner || inner === element) break;
    element = inner;
  }
  return element;
}

// Every loop is bounded by a count or a character budget, not by the size of
// the element: a pointer resting on <body> must cost the same as one on a link.
// No form control's value is read, from its property or its attribute.
function describe(element) {
  const secretName = /token|secret|passw(?:or)?d|api[_-]?key|session[_-]?id|csrf|signature|credential/i;
  const secretPair = /(?:token|secret|passw(?:or)?d|api[_-]?key|session[_-]?id|csrf|signature|credential|x-amz-)[\w-]*=/i;
  const clip = (value, max) => {
    const text = String(value ?? '');
    return text.length > max ? `${text.slice(0, max)}…` : text;
  };
  const tag = element.localName;
  const boundedText = (root, max) => {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    let text = '';
    for (let node = walker.nextNode(), seen = 0; node && seen < 80 && text.length <= max; node = walker.nextNode(), seen++) {
      if (['script', 'style', 'noscript', 'textarea'].includes(node.parentElement?.localName)) continue;
      const part = node.nodeValue.slice(0, max + 1).replace(/\s+/g, ' ').trim();
      if (part) text += (text ? ' ' : '') + part;
    }
    return clip(text, max);
  };

  const implicit = { a: element.hasAttribute('href') ? 'link' : '', button: 'button', textarea: 'textbox', select: 'combobox', img: 'img', nav: 'navigation', main: 'main', h1: 'heading', h2: 'heading', h3: 'heading', h4: 'heading', h5: 'heading', h6: 'heading', input: ['checkbox', 'radio'].includes(element.type) ? element.type : ['button', 'submit'].includes(element.type) ? 'button' : 'textbox' };
  const labelledBy = (element.getAttribute('aria-labelledby') ?? '').slice(0, 2048).split(/\s+/).slice(0, 20).map((id) => { const target = id && document.getElementById(id); return target ? boundedText(target, 100) : ''; }).join(' ').trim();
  const labels = element.labels ? [...element.labels].slice(0, 4).map((label) => boundedText(label, 100)).join(' ') : '';
  const name = element.getAttribute('aria-label') || labelledBy || labels || element.getAttribute('alt') || element.getAttribute('title') || (['button', 'a', 'label', 'summary'].includes(tag) ? boundedText(element, 200) : '');

  const stableClasses = (node, count) => [...node.classList].slice(0, 32).filter((value) => value.length <= 60 && !/^css-[a-z0-9]+$/i.test(value) && !(/^[\w-]{12,}$/.test(value) && /\d/.test(value) && /[A-Z]/.test(value))).slice(0, count);
  const part = (node) => node.id ? `${node.localName}#${CSS.escape(node.id)}` : node.localName + stableClasses(node, 2).map((value) => `.${CSS.escape(value)}`).join('');
  const root = element.getRootNode();
  const unique = (selector) => { try { return root.querySelectorAll(selector).length === 1; } catch { return false; } };
  const nth = (node) => {
    let index = 1;
    let alone = true;
    for (let sibling = node.previousElementSibling; sibling; sibling = sibling.previousElementSibling) if (sibling.localName === node.localName) index++;
    for (let sibling = node.nextElementSibling; sibling && alone; sibling = sibling.nextElementSibling) if (sibling.localName === node.localName) alone = false;
    return index > 1 || !alone ? `:nth-of-type(${index})` : '';
  };
  const parts = [];
  let selector = '';
  for (let node = element; node && node.localName !== 'body' && node.localName !== 'html' && parts.length < 10; node = node.parentElement) {
    let own = part(node);
    if (!unique([own, ...parts].join(' > '))) own += nth(node);
    parts.unshift(own);
    selector = parts.join(' > ');
    if (unique(selector)) break;
  }

  const attributes = {};
  for (const attribute of [...element.attributes].slice(0, 64)) {
    const key = attribute.name.toLowerCase();
    if (!['id', 'class', 'name', 'type', 'role', 'href', 'src', 'alt', 'title', 'placeholder', 'for', 'action', 'method', 'disabled', 'data-testid', 'data-test'].includes(key) && !key.startsWith('aria-')) continue;
    attributes[key] = secretPair.test(attribute.value) ? '[redacted]' : clip(attribute.value, 200);
  }

  let budget = 2000;
  let html = '';
  const emit = (text) => {
    if (budget <= 0) return;
    html += text.length > budget ? `${text.slice(0, budget)}…` : text;
    budget -= text.length;
  };
  const escape = (text) => text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
  const write = (node) => {
    if (budget <= 0) return;
    if (node.nodeType === Node.TEXT_NODE) {
      const text = node.nodeValue.slice(0, budget + 1).replace(/\s+/g, ' ');
      if (text.trim()) emit(escape(text));
      return;
    }
    if (node.nodeType !== Node.ELEMENT_NODE || ['head', 'script', 'style', 'noscript'].includes(node.localName)) return;
    let open = `<${node.localName}`;
    for (const attribute of [...node.attributes].slice(0, 32)) {
      const key = attribute.name.toLowerCase();
      if (key === 'value' || key === 'content' || key.startsWith('on')) continue;
      const value = secretName.test(key) || secretPair.test(attribute.value) ? '[redacted]' : clip(attribute.value, 120);
      open += value ? ` ${key}="${escape(value)}"` : ` ${key}`;
    }
    emit(`${open}>`);
    if (['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr'].includes(node.localName)) return;
    if (node.localName !== 'textarea') for (let child = node.firstChild; child && budget > 0; child = child.nextSibling) write(child);
    emit(`</${node.localName}>`);
  };
  write(element);

  const computed = getComputedStyle(element);
  const styles = {};
  for (const property of ['display', 'position', 'width', 'height', 'margin', 'padding', 'color', 'background-color', 'border', 'border-radius', 'font-family', 'font-size', 'font-weight', 'line-height']) {
    const value = computed.getPropertyValue(property);
    if (value) styles[property] = clip(value, 120);
  }

  const fiberKey = Object.keys(element).find((key) => key.startsWith('__reactFiber$') || key.startsWith('__reactInternalInstance$'));
  const components = [];
  let source = '';
  for (let fiber = fiberKey ? element[fiberKey] : null, depth = 0; fiber && depth < 35; fiber = fiber.return, depth++) {
    const type = fiber.type || fiber.elementType;
    const named = type && typeof type !== 'string' ? type.displayName || type.name || type.render?.displayName || type.render?.name || type.type?.displayName || type.type?.name : '';
    const wrapper = !named || named.length <= 2 || /^(Fragment|Root|Routes|Route|Outlet|Provider|Consumer|Profiler|Suspense)$/.test(named) || /(?:Boundary|BoundaryHandler|Router|Provider|Consumer|Context|Wrapper)$/.test(named) || /^(Inner|Outer|Client|Server|RSC|Dev|React|Hot)/.test(named);
    if (!wrapper && !components.includes(named) && components.length < 6) components.unshift(named);
    const origin = fiber._debugSource || fiber._debugOwner?._debugSource;
    if (!source && origin?.fileName && origin.lineNumber) {
      const file = String(origin.fileName).replace(/[?#].*$/, '').replace(/^turbopack:\/\/\/\[project\]\//, '').replace(/^webpack-internal:\/\/\/(?:\([^)]+\)\/)?(?:\.\/)?/, '').replace(/^webpack:\/\/\/(?:\.\/)?/, '').replace(/^turbopack:\/\/\//, '').replace(/^https?:\/\/[^/]+\//, '').replace(/^file:\/\/\//, '/').replace(/^\.\//, '');
      source = `${file}:${origin.lineNumber}${origin.columnNumber === undefined ? '' : `:${origin.columnNumber}`}`;
    }
  }

  const rect = element.getBoundingClientRect();
  return {
    tag,
    role: element.getAttribute('role') || implicit[tag] || '',
    name,
    text: boundedText(element, 300),
    selector: selector || tag,
    attributes,
    bounds: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
    html,
    styles,
    component: components.join(' > '),
    source,
    inset: { x: rect.x + element.clientLeft, y: rect.y + element.clientTop },
  };
}

const short = (value, max) => (typeof value === 'string' ? value.slice(0, max) : '');
const finite = (value) => (Number.isFinite(value) ? value : 0);
const strings = (value, entries, max) => Object.fromEntries(Object.entries(value && typeof value === 'object' ? value : {}).slice(0, entries).map(([key, text]) => [short(key, 64), short(text, max)]));

// Returns the element under a viewport point, following same-page frames, or
// null when nothing is there. The page computed the description, so every
// field is bounded again here before it leaves the companion.
export async function inspectPoint(page, x, y, read) {
  let frame = page.mainFrame();
  let left = 0;
  let top = 0;
  for (let depth = 0; ; depth++) {
    const handle = (await read(frame.evaluateHandle(pick, { x: x - left, y: y - top }))).asElement();
    if (!handle) return null;
    try {
      const found = await read(handle.evaluate(describe));
      const child = ['iframe', 'frame'].includes(found.tag) && depth < 3 ? await read(handle.contentFrame()) : null;
      if (child) {
        frame = child;
        left += finite(found.inset?.x);
        top += finite(found.inset?.y);
        continue;
      }
      return {
        tag: short(found.tag, 64),
        role: short(found.role, 64),
        name: short(found.name, 256),
        text: short(found.text, 512),
        selector: short(found.selector, 1024),
        attributes: strings(found.attributes, 32, 256),
        bounds: { x: finite(found.bounds?.x) + left, y: finite(found.bounds?.y) + top, width: finite(found.bounds?.width), height: finite(found.bounds?.height) },
        html: short(found.html, 2048),
        styles: strings(found.styles, 16, 128),
        component: short(found.component, 512),
        source: short(found.source, 512),
        frame_url: depth ? short(frame.url(), 512) : '',
      };
    } finally { void handle.dispose().catch(() => {}); }
  }
}
