import { Tooltip as TooltipPrimitive } from 'radix-ui'
import * as React from 'react'
import { flushSync } from 'react-dom'

// Radix opens a tooltip on any focus; only a focus that a navigation key just caused shows one, so a
// dialog that opens on its own and focuses a control does not.
const navigationKeys = new Set(['Tab', 'ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Home', 'End'])
let navigating = false
document.addEventListener(
  'keydown',
  (event) => {
    navigating = navigationKeys.has(event.key)
    if (navigating) setTimeout(() => (navigating = false))
  },
  true,
)
document.addEventListener('pointerdown', () => (navigating = false), true)

// An open tooltip is the top Radix layer, and only the top layer hears Escape. Closing it synchronously
// before the key reaches the document makes the dialog or drawer under it the top layer again, so one
// Escape closes both; a closing tooltip must not animate, or Radix keeps it mounted and on top. Marking
// the key handled after those layers have run keeps a shell binding on Escape from firing too.
const openTooltips = new Set<() => void>()
window.addEventListener(
  'keydown',
  (event) => {
    if (event.key !== 'Escape' || openTooltips.size === 0) return
    flushSync(() => {
      for (const close of openTooltips) close()
    })
    document.addEventListener('keydown', (later) => later === event && event.preventDefault(), {
      capture: true,
      once: true,
    })
  },
  true,
)

export function Tooltip({
  content,
  side = 'top',
  disabled = false,
  children,
}: {
  content: React.ReactNode
  side?: React.ComponentProps<typeof TooltipPrimitive.Content>['side']
  disabled?: boolean
  children: React.ReactElement
}) {
  const [open, setOpen] = React.useState(false)
  React.useEffect(() => {
    if (!open) return
    const close = () => setOpen(false)
    openTooltips.add(close)
    return () => {
      openTooltips.delete(close)
    }
  }, [open])

  if (disabled) return children
  return (
    <TooltipPrimitive.Provider delayDuration={300}>
      <TooltipPrimitive.Root open={open} onOpenChange={setOpen}>
        <TooltipPrimitive.Trigger
          asChild
          onFocus={(event) => {
            if (!navigating) event.preventDefault()
          }}
        >
          {children}
        </TooltipPrimitive.Trigger>
        <TooltipPrimitive.Portal>
          <TooltipPrimitive.Content
            data-slot="tooltip"
            side={side}
            sideOffset={4}
            collisionPadding={8}
            className="z-50 max-w-xs animate-fade-in rounded-control border border-seam bg-raised px-2 py-1 text-ui-sm whitespace-pre-line text-text shadow-overlay data-[state=closed]:animate-none motion-reduce:animate-none"
          >
            {content}
          </TooltipPrimitive.Content>
        </TooltipPrimitive.Portal>
      </TooltipPrimitive.Root>
    </TooltipPrimitive.Provider>
  )
}
