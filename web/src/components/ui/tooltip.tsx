import { Tooltip as TooltipPrimitive } from 'radix-ui'
import type * as React from 'react'

// Radix opens a tooltip on any focus; a dialog focusing its first control would open one that swallows its Escape.
const navigationKeys = new Set(['Tab', 'ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Home', 'End'])
let navigating = false
document.addEventListener('keydown', (event) => { navigating = navigationKeys.has(event.key) }, true)
document.addEventListener('pointerdown', () => { navigating = false }, true)
document.addEventListener('click', () => { navigating = false }, true)

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
  if (disabled) return children
  return (
    <TooltipPrimitive.Provider delayDuration={300}>
      <TooltipPrimitive.Root>
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
            className="z-50 max-w-xs animate-fade-in rounded-control border border-seam bg-raised px-2 py-1 text-ui-sm whitespace-pre-line text-text shadow-overlay motion-reduce:animate-none"
          >
            {content}
          </TooltipPrimitive.Content>
        </TooltipPrimitive.Portal>
      </TooltipPrimitive.Root>
    </TooltipPrimitive.Provider>
  )
}
