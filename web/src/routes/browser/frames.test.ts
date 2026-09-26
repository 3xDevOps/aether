import { describe, expect, it } from 'vitest'
import type { DevBrowserFrameMetadata } from '@/lib/types'
import { decodeBrowserFrame, framePoint, sameFrameTarget } from './frames'

const frame: DevBrowserFrameMetadata = {
  run_id: 'run', session_id: 'session', page_id: 'page', page_revision: 4,
  viewport_id: 'phone', width: 390, height: 844, sequence: 9,
  mime_type: 'image/jpeg', timestamp: '2026-09-26T00:00:00Z',
}

describe('displayed browser frame coordinates', () => {
  it('rejects letterbox clicks and maps a scaled phone viewport without document scroll offsets', () => {
    const rect = { left: 10, top: 20, width: 400, height: 422 }
    expect(framePoint({ ...frame, scroll_y: 800 }, rect, 112.5, 20)).toEqual({ x: 0, y: 0 })
    expect(framePoint(frame, rect, 210, 231)).toEqual({ x: 195, y: 422 })
    expect(framePoint(frame, rect, 100, 231)).toBeNull()
    expect(framePoint(frame, rect, 307.5, 231)).toBeNull()
  })

  it('undoes screencast page scale and browser top offset', () => {
    const scaled = { ...frame, page_scale_factor: 2, offset_top: 20 }
    expect(framePoint(scaled, { left: 0, top: 0, width: 390, height: 844 }, 100, 220)).toEqual({ x: 50, y: 100 })
    expect(framePoint(scaled, { left: 0, top: 0, width: 390, height: 844 }, 100, 10)).toBeNull()
  })

  it('invalidates pending input across navigation, resize, page or run changes but not a newer same-target frame', () => {
    expect(sameFrameTarget(frame, { ...frame, sequence: 10 })).toBe(true)
    for (const changed of [{ page_revision: 5 }, { viewport_id: 'desktop' }, { page_id: 'popup' }, { session_id: 'reset' }, { run_id: 'another-run' }]) {
      expect(sameFrameTarget(frame, { ...frame, ...changed })).toBe(false)
    }
  })
})

describe('binary browser frame bounds', () => {
  it('rejects oversized and incomplete records before reading payloads', () => {
    const header = new ArrayBuffer(8)
    const view = new DataView(header)
    view.setUint32(0, 16 * 1024 + 1)
    view.setUint32(4, 1)
    expect(() => decodeBrowserFrame(header)).toThrow('Invalid browser frame size')
    view.setUint32(0, 1)
    view.setUint32(4, 2 * 1024 * 1024 + 1)
    expect(() => decodeBrowserFrame(header)).toThrow('Invalid browser frame size')
    view.setUint32(4, 1)
    expect(() => decodeBrowserFrame(header)).toThrow('Invalid browser frame size')
  })
})
