import { Monitor, Smartphone, Tablet, type LucideIcon } from '@/components/icons'

export interface ViewportSize {
  width: number
  height: number
}

export const viewportPresets: Record<string, ViewportSize & { label: string; Icon: LucideIcon }> = {
  desktop: { label: 'Desktop', Icon: Monitor, width: 1280, height: 800 },
  tablet: { label: 'Tablet', Icon: Tablet, width: 820, height: 1180 },
  phone: { label: 'Phone', Icon: Smartphone, width: 390, height: 844 },
}

/** The viewport that fills a pane, inside what dev.browser.viewport accepts.
 * Null until the pane has been measured. */
export function paneViewport(pane: ViewportSize): ViewportSize | null {
  if (pane.width < 1 || pane.height < 1) return null
  return {
    width: Math.min(2560, Math.max(240, pane.width)),
    height: Math.min(1600, Math.max(240, pane.height)),
  }
}
