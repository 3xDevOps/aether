import type { DevBrowserFrameMetadata } from '@/lib/types'

export interface BrowserFrame {
  metadata: DevBrowserFrameMetadata
  image: Blob
}

/** One websocket message is one frame; never concatenate an unbounded stream. */
export function decodeBrowserFrame(data: ArrayBuffer): BrowserFrame {
  if (data.byteLength < 8) throw new Error('Incomplete browser frame')
  const header = new DataView(data)
  const metadataBytes = header.getUint32(0)
  const imageBytes = header.getUint32(4)
  if (!metadataBytes || metadataBytes > 16 * 1024 || !imageBytes || imageBytes > 2 * 1024 * 1024 || data.byteLength !== 8 + metadataBytes + imageBytes) {
    throw new Error('Invalid browser frame size')
  }
  const metadata = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(new Uint8Array(data, 8, metadataBytes))) as DevBrowserFrameMetadata
  if (!metadata.run_id || !metadata.session_id || !metadata.page_id || !metadata.viewport_id ||
    !Number.isSafeInteger(metadata.page_revision) || metadata.page_revision < 1 ||
    !Number.isSafeInteger(metadata.sequence) || metadata.sequence < 1 ||
    !Number.isInteger(metadata.width) || metadata.width < 1 || metadata.width > 2560 ||
    !Number.isInteger(metadata.height) || metadata.height < 1 || metadata.height > 1600 ||
    !['image/jpeg', 'image/png'].includes(metadata.mime_type) ||
    !Number.isFinite(metadata.offset_top ?? 0) ||
    !Number.isFinite(metadata.page_scale_factor ?? 1) || (metadata.page_scale_factor ?? 1) <= 0) {
    throw new Error('Invalid browser frame metadata')
  }
  return { metadata, image: new Blob([new Uint8Array(data, 8 + metadataBytes, imageBytes)], { type: metadata.mime_type }) }
}

export interface FrameRect { left: number; top: number; width: number; height: number }

/** Canvas CSS uses contain. Ignore letterboxing, then undo the screencast scale. */
export function framePoint(frame: DevBrowserFrameMetadata, rect: FrameRect, clientX: number, clientY: number): { x: number; y: number } | null {
  const scale = Math.min(rect.width / frame.width, rect.height / frame.height)
  if (!(scale > 0)) return null
  const pixelX = (clientX - rect.left - (rect.width - frame.width * scale) / 2) / scale
  const pixelY = (clientY - rect.top - (rect.height - frame.height * scale) / 2) / scale
  const pageScale = frame.page_scale_factor ?? 1
  const x = pixelX / pageScale
  const y = (pixelY - (frame.offset_top ?? 0)) / pageScale
  if (pixelX < 0 || pixelY < 0 || pixelX >= frame.width || pixelY >= frame.height || x < 0 || y < 0 || x >= frame.width || y >= frame.height) return null
  return { x, y }
}

export function sameFrameTarget(a: DevBrowserFrameMetadata, b: DevBrowserFrameMetadata): boolean {
  return a.run_id === b.run_id && a.session_id === b.session_id && a.page_id === b.page_id && a.page_revision === b.page_revision && a.viewport_id === b.viewport_id
}
