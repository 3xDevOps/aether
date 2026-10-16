import { useMediaQuery } from '@/lib/hooks'

export const MOBILE_MAX_WIDTH = 767

export const mobileScreen = `(max-width: ${MOBILE_MAX_WIDTH}px)`

export function useIsMobile(): boolean {
  return useMediaQuery(mobileScreen)
}

/** The narrowest a run's content stays beside a docked panel. Narrower, the panel overlays instead. */
export const minReadableWidth = 560
