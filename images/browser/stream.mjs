import { limits, requireValue } from './session.mjs';

// Wire record: uint32be JSON length, uint32be image length, JSON metadata, JPEG.
// Each viewer retains at most one pending record, replaced by the newest frame.
export function encodeFrame(metadata, bytes) {
  const header = Buffer.from(JSON.stringify(metadata));
  requireValue(header.length <= 16384 && bytes.length > 0 && bytes.length <= limits.frame, 'Frame exceeds transport limit', 'resource_limit');
  const prefix = Buffer.allocUnsafe(8);
  prefix.writeUInt32BE(header.length, 0);
  prefix.writeUInt32BE(bytes.length, 4);
  return Buffer.concat([prefix, header, bytes]);
}

export async function streamPage(session, request, response) {
  const state = session.target(request);
  requireValue(state.streams.size < 4, 'Maximum browser viewers reached', 'resource_limit');
  const cdp = await session.cdp(state);
  requireValue(!response.destroyed, 'Browser viewer disconnected', 'cancelled');
  // Getting the CDP session can yield to navigation; never attach to a stale page.
  session.target(request);
  let latest = null;
  let blocked = false;
  let ended = false;
  let ready = false;
  let lastSent = 0;
  let sequence = 0;
  let timer;
  const flush = () => {
    if (ended || !ready || blocked || !latest || Date.now() - lastSent < 100) return;
    const frame = latest;
    latest = null;
    blocked = !response.write(frame);
    lastSent = Date.now();
  };
  const drained = () => { blocked = false; flush(); };
  const cleanup = () => {
    if (ended) return;
    ended = true;
    latest = null;
    clearInterval(timer);
    response.off('drain', drained);
    state.streams.delete(viewer);
    if (!state.streams.size) {
      cdp.off('Page.screencastFrame', state.frameListener);
      void cdp.send('Page.stopScreencast').catch(() => {});
    }
  };
  const viewer = {
    end: () => { cleanup(); response.end(); },
    invalidate: () => { latest = null; },
    frame: (event) => {
      if (ended || !Number.isFinite(event.metadata.timestamp) || event.metadata.timestamp < state.viewportChanged) return;
      const size = state.page.viewportSize();
      if (event.metadata.deviceWidth !== size.width || event.metadata.deviceHeight !== size.height) return;
      if (event.data.length > Math.ceil(limits.frame * 4 / 3)) return;
      const bytes = Buffer.from(event.data, 'base64');
      if (!bytes.length || bytes.length > limits.frame) return;
      latest = encodeFrame({ session_id: session.sessionID, page_id: state.id, page_revision: state.revision, viewport_id: state.viewportID, ...size, content_type: 'image/jpeg', captured_at: new Date(event.metadata.timestamp * 1000).toISOString(), sequence: ++sequence, offset_top: event.metadata.offsetTop, page_scale_factor: event.metadata.pageScaleFactor, scroll_x: event.metadata.scrollOffsetX, scroll_y: event.metadata.scrollOffsetY }, bytes);
      flush();
    },
  };
  const first = state.streams.size === 0;
  state.streams.add(viewer);
  response.once('close', cleanup);
  response.on('drain', drained);
  try {
    if (first) {
      state.frameListener = (event) => {
        // Slow observers cannot stall Chromium: acknowledge before forwarding.
        void cdp.send('Page.screencastFrameAck', { sessionId: event.sessionId }).catch(() => {});
        for (const stream of state.streams) stream.frame(event);
      };
      cdp.on('Page.screencastFrame', state.frameListener);
      await cdp.send('Page.startScreencast', { format: 'jpeg', quality: 75, maxWidth: 2560, maxHeight: 1600, everyNthFrame: 1 });
    }
    if (ended) return;
    response.writeHead(200, { 'Content-Type': 'application/x-aether-browser-frames', 'Cache-Control': 'no-store' });
    response.flushHeaders();
    ready = true;
    timer = setInterval(flush, 100);
    flush();
  } catch (error) { cleanup(); throw error; }
}
