import { useCallback, useLayoutEffect, useRef } from 'react'

type BoardView = 'cards' | 'map'
type Bounds = { left: number; top: number; right: number; bottom: number }
type CardFrame = {
  element: HTMLElement
  rect: DOMRect
  clip: Bounds
  visible: boolean
  clone?: HTMLElement
  width: number
  height: number
}
type MovingCard = { shell: HTMLElement; card: HTMLElement }
type Transition = {
  cards: Map<string, MovingCard>
  cancel: () => void
}

const duration = 460
const reducedMotion = '(prefers-reduced-motion: reduce)'

function intersect(a: Bounds, b: Bounds): Bounds {
  return {
    left: Math.max(a.left, b.left),
    top: Math.max(a.top, b.top),
    right: Math.min(a.right, b.right),
    bottom: Math.min(a.bottom, b.bottom),
  }
}

function boardBounds(board: HTMLElement): Bounds {
  return intersect(board.getBoundingClientRect(), {
    left: 0,
    top: 0,
    right: document.documentElement.clientWidth,
    bottom: window.innerHeight,
  })
}

function visible(rect: DOMRect, clip: Bounds) {
  return rect.width > 0 && rect.height > 0 &&
    Math.min(rect.right, clip.right) > Math.max(rect.left, clip.left) &&
    Math.min(rect.bottom, clip.bottom) > Math.max(rect.top, clip.top)
}

function visualClone(card: HTMLElement) {
  const clone = card.cloneNode(true) as HTMLElement
  // The overlay is purely visual: no duplicate run identities, DOM IDs or
  // focusable controls may escape into the live board or accessibility tree.
  for (const element of [clone, ...clone.querySelectorAll<HTMLElement>('*')]) {
    for (const attribute of ['id', 'data-run-id', 'aria-controls', 'aria-labelledby', 'aria-describedby']) {
      element.removeAttribute(attribute)
    }
  }
  clone.inert = true
  clone.setAttribute('aria-hidden', 'true')
  Object.assign(clone.style, {
    width: '100%', height: '100%', minWidth: '0', minHeight: '0',
    maxWidth: 'none', maxHeight: 'none', margin: '0', transform: 'none',
    position: 'relative', inset: 'auto', overflow: 'hidden', opacity: '1',
    pointerEvents: 'none', transition: 'none',
  })
  return clone
}

function frames(board: HTMLElement, cloneVisible: boolean) {
  const bounds = boardBounds(board)
  const result = new Map<string, CardFrame>()
  const clips = new Map<HTMLElement, Bounds>([[board, bounds]])
  function clipFor(element: HTMLElement): Bounds {
    const cached = clips.get(element)
    if (cached) return cached
    const parent = element.parentElement
    const inherited = parent ? clipFor(parent) : bounds
    const style = getComputedStyle(element)
    const rect = element.getBoundingClientRect()
    const clip = {
      left: /auto|scroll|hidden|clip/.test(style.overflowX) ? Math.max(inherited.left, rect.left) : inherited.left,
      right: /auto|scroll|hidden|clip/.test(style.overflowX) ? Math.min(inherited.right, rect.right) : inherited.right,
      top: /auto|scroll|hidden|clip/.test(style.overflowY) ? Math.max(inherited.top, rect.top) : inherited.top,
      bottom: /auto|scroll|hidden|clip/.test(style.overflowY) ? Math.min(inherited.bottom, rect.bottom) : inherited.bottom,
    }
    clips.set(element, clip)
    return clip
  }
  for (const element of board.querySelectorAll<HTMLElement>('article[data-run-id]')) {
    const id = element.dataset.runId
    if (!id) continue
    const rect = element.getBoundingClientRect()
    const clip = element.parentElement ? clipFor(element.parentElement) : bounds
    const onScreen = visible(rect, clip)
    result.set(id, {
      element, rect, clip, visible: onScreen,
      clone: cloneVisible && onScreen ? visualClone(element) : undefined,
      width: element.offsetWidth, height: element.offsetHeight,
    })
  }
  return result
}

// An offscreen card travels through the corresponding board edge, rather than
// spending almost the whole animation crossing thousands of invisible pixels.
function nearBoard(rect: DOMRect, bounds: Bounds) {
  return {
    left: Math.max(bounds.left - rect.width, Math.min(bounds.right, rect.left)),
    top: Math.max(bounds.top - rect.height, Math.min(bounds.bottom, rect.top)),
    width: rect.width,
    height: rect.height,
  }
}

function clipped(frame: CardFrame) {
  const { rect, clip } = frame
  const percent = (distance: number, size: number) => Math.min(100, Math.max(0, distance / size * 100))
  return `inset(${percent(clip.top - rect.top, rect.height)}% ${percent(rect.right - clip.right, rect.width)}% ${percent(rect.bottom - clip.bottom, rect.height)}% ${percent(clip.left - rect.left, rect.width)}%)`
}

/** Capture before the store update; animate screen-space geometry after commit. */
export function useBoardTransition(
  view: BoardView,
  setView: (view: BoardView) => void,
  scope: string,
) {
  const boardRef = useRef<HTMLDivElement>(null)
  const pending = useRef<{ view: BoardView; scope: string; cards: Map<string, CardFrame> } | null>(null)
  const active = useRef<Transition | null>(null)

  const changeView = useCallback((next: BoardView) => {
    if (next === view) return
    const board = boardRef.current
    pending.current = null
    if (board && !window.matchMedia(reducedMotion).matches && typeof board.animate === 'function') {
      const cards = frames(board, true)
      // A rapid reversal starts at the current animated positions, not at the
      // hidden destination layout. Capture before cancelling the old overlay.
      const bounds = boardBounds(board)
      active.current?.cards.forEach(({ shell, card }, id) => {
        const frame = cards.get(id)
        if (!frame) return
        const rect = shell.getBoundingClientRect()
        cards.set(id, {
          ...frame, rect, clip: bounds, visible: visible(rect, bounds),
          clone: visualClone(card), width: shell.offsetWidth, height: shell.offsetHeight,
        })
      })
      pending.current = { view: next, scope, cards }
    }
    active.current?.cancel()
    setView(next)
  }, [view, setView, scope])

  useLayoutEffect(() => {
    active.current?.cancel()
    const before = pending.current
    pending.current = null
    const board = boardRef.current
    if (!before || before.view !== view || before.scope !== scope || !board || window.matchMedia(reducedMotion).matches) return
    const after = frames(board, false)
    const bounds = boardBounds(board)
    if (bounds.right <= bounds.left || bounds.bottom <= bounds.top) return
    const overlay = document.createElement('div')
    overlay.setAttribute('aria-hidden', 'true')
    overlay.inert = true
    Object.assign(overlay.style, {
      position: 'fixed', left: `${bounds.left}px`, top: `${bounds.top}px`,
      width: `${bounds.right - bounds.left}px`, height: `${bounds.bottom - bounds.top}px`,
      overflow: 'hidden', pointerEvents: 'none', zIndex: '40', contain: 'strict',
    })
    document.body.append(overlay)
    const animations: Animation[] = []
    const hidden: { element: HTMLElement; opacity: string }[] = []
    const moving = new Map<string, MovingCard>()
    const media = window.matchMedia(reducedMotion)
    let timer = 0
    let scrollFrame = 0
    let disposed = false
    const cancel = () => {
      if (disposed) return
      disposed = true
      window.clearTimeout(timer)
      window.cancelAnimationFrame(scrollFrame)
      for (const animation of animations) animation.cancel()
      for (const { element, opacity } of hidden) element.style.opacity = opacity
      overlay.remove()
      window.removeEventListener('resize', cancel)
      window.removeEventListener('scroll', onScroll, true)
      board.removeEventListener('pointerdown', cancel, true)
      board.removeEventListener('keydown', cancel, true)
      board.removeEventListener('wheel', cancel, true)
      media.removeEventListener('change', cancel)
      if (active.current?.cancel === cancel) active.current = null
    }
    const onScroll = (event: Event) => {
      const target = event.target
      if (target instanceof Node && (target.contains(board) || board.contains(target))) cancel()
    }
    active.current = { cards: moving, cancel }
    window.addEventListener('resize', cancel)
    // Layout replacement can reset a scroll container before the first paint;
    // that is already reflected in the measured destination, not user input.
    scrollFrame = window.requestAnimationFrame(() => {
      window.addEventListener('scroll', onScroll, true)
    })
    board.addEventListener('pointerdown', cancel, true)
    board.addEventListener('keydown', cancel, true)
    board.addEventListener('wheel', cancel, true)
    media.addEventListener('change', cancel)

    for (const id of new Set([...before.cards.keys(), ...after.keys()])) {
      const source = before.cards.get(id)
      const destination = after.get(id)
      if (!source?.visible && !destination?.visible) continue
      const template = source?.clone ? source : destination
      if (!template || !template.width || !template.height) continue
      const card = template.clone ?? visualClone(template.element)
      const shell = document.createElement('div')
      Object.assign(shell.style, {
        position: 'absolute', left: '0', top: '0',
        width: `${template.width}px`, height: `${template.height}px`,
        transformOrigin: '0 0', pointerEvents: 'none', overflow: 'hidden',
      })
      shell.append(card)
      overlay.append(shell)
      moving.set(id, { shell, card })
      if (destination) {
        hidden.push({ element: destination.element, opacity: destination.element.style.opacity })
        destination.element.style.opacity = '0'
      }
      const startFrame = source ?? destination!
      const endFrame = destination ?? startFrame
      const start = nearBoard(startFrame.rect, bounds)
      const end = nearBoard(endFrame.rect, bounds)
      // Reflow the shell's layout dimensions; only camera zoom scales its text.
      // On reversal, the captured shell size preserves the current uniform zoom.
      const startScale = startFrame.rect.width / startFrame.width
      const endScale = endFrame.rect.width / endFrame.width
      const geometry = (rect: typeof start, scale: number) => ({
        width: `${rect.width / scale}px`,
        height: `${rect.height / scale}px`,
        transform: `translate(${rect.left - bounds.left}px, ${rect.top - bounds.top}px) scale(${scale})`,
      })
      const fromClip = source ? clipped(source) : 'inset(0%)'
      const toClip = destination ? clipped(destination) : 'inset(0%)'
      animations.push(shell.animate([
        { ...geometry(start, startScale), clipPath: fromClip, opacity: source ? 1 : 0 },
        { clipPath: 'inset(0%)', offset: 0.2 },
        { clipPath: 'inset(0%)', offset: 0.8 },
        { ...geometry(end, endScale), clipPath: toClip, opacity: destination ? 1 : 0 },
      ], { duration, easing: 'cubic-bezier(0.22, 1, 0.36, 1)', fill: 'both' }))
    }
    // The timeout also restores interactions if a browser pauses its animation
    // timeline. Input/scroll cancels synchronously and leaves the real cards live.
    timer = window.setTimeout(cancel, duration + 120)
    void Promise.all(animations.map((animation) => animation.finished)).then(cancel, cancel)
  }, [view, scope])

  useLayoutEffect(() => () => {
    pending.current = null
    active.current?.cancel()
  }, [])

  return { boardRef, changeView }
}
