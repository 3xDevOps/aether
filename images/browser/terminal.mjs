import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { limits, requireValue, boundedInteger } from './session.mjs';

const root = new URL('./', import.meta.url);
let assets;
async function rendererAssets() {
  assets ||= Promise.all([
    readFile(new URL('node_modules/@xterm/xterm/lib/xterm.js', root), 'utf8'),
    readFile(new URL('node_modules/@xterm/xterm/css/xterm.css', root), 'utf8'),
    readFile(new URL('fonts/jetbrains-mono-nfm-regular.woff2', root)),
    readFile(new URL('fonts/jetbrains-mono-nfm-bold.woff2', root)),
  ]);
  return assets;
}

export async function renderTerminal(browser, request) {
  requireValue(typeof request.vt === 'string' && Buffer.byteLength(request.vt) <= limits.request - 4096, 'Terminal VT exceeds capture limit');
  requireValue(typeof request.session_id === 'string' && request.session_id.length <= 256 && request.session_id.length > 0, 'Terminal session identity required');
  requireValue(Number.isSafeInteger(request.screen_revision) && request.screen_revision >= 0, 'Terminal screen revision required');
  // xterm does not implement Sixel, Kitty graphics or iTerm inline images.
  requireValue(!/\x1bP[^\x1b]*q|\x90[^\x9c]*q|\x1b_G|\x1b\]1337;File=/.test(request.vt), 'Terminal graphics protocol is not supported by the PNG renderer', 'unsupported_graphics');
  const cols = boundedInteger(request.cols, 80, 1, 320);
  const rows = boundedInteger(request.rows, 24, 1, 120);
  const fontSize = boundedInteger(request.font_size, 14, 8, 24);
  const [script, css, regular, bold] = await rendererAssets();
  const context = await browser.newContext({ viewport: { width: 4096, height: 4096 }, offline: true, serviceWorkers: 'block', javaScriptEnabled: true, acceptDownloads: false });
  try {
    await context.route('**/*', (route) => route.abort('blockedbyclient'));
    const page = await context.newPage();
    await page.setContent('<!doctype html><html><head></head><body><div id="terminal"></div></body></html>');
    await page.addStyleTag({ content: `${css}\n@font-face{font-family:AetherTerminal;src:url(data:font/woff2;base64,${regular.toString('base64')}) format('woff2');font-weight:400}@font-face{font-family:AetherTerminal;src:url(data:font/woff2;base64,${bold.toString('base64')}) format('woff2');font-weight:700}html,body{margin:0;background:#09090b}#terminal{display:inline-block;padding:8px}.xterm-viewport{overflow:hidden!important}` });
    await page.addScriptTag({ content: script });
    const dimensions = await page.evaluate(async ({ vt, cols, rows, fontSize }) => {
      await Promise.all([document.fonts.load(`${fontSize}px AetherTerminal`), document.fonts.load(`bold ${fontSize}px AetherTerminal`)]);
      await document.fonts.ready;
      const terminal = new window.Terminal({ cols, rows, fontSize, fontFamily: 'AetherTerminal', scrollback: 0, cursorBlink: false, allowProposedApi: false, theme: { background: '#09090b', foreground: '#fafafa' } });
      terminal.open(document.getElementById('terminal'));
      // No onData/onBinary responder is attached to this read-only replay.
      await new Promise((resolve) => terminal.write(vt, resolve));
      terminal.focus();
      await document.fonts.ready;
      terminal.refresh(0, rows - 1);
      await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      const rect = document.getElementById('terminal').getBoundingClientRect();
      return { width: Math.ceil(rect.width), height: Math.ceil(rect.height), active_buffer: terminal.buffer.active.type };
    }, { vt: request.vt, cols, rows, fontSize });
    requireValue(dimensions.width <= 8192 && dimensions.height <= 4096, 'Terminal image dimensions exceed limit', 'resource_limit');
    await page.setViewportSize({ width: dimensions.width, height: dimensions.height });
    const bytes = await page.locator('#terminal').screenshot({ type: 'png', timeout: limits.timeout });
    requireValue(bytes.length <= limits.image, 'Terminal PNG exceeds image limit', 'resource_limit');
    return { bytes, metadata: { content_type: 'image/png', session_id: request.session_id, screen_revision: request.screen_revision, output_position: request.output_position, cols, rows, ...dimensions, captured_at: request.captured_at || new Date().toISOString() } };
  } finally { await context.close(); }
}
