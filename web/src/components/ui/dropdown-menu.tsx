import type * as React from "react"
import { DropdownMenu as DropdownMenuPrimitive } from "radix-ui"

import { cn, focusRing } from "@/lib/utils"

function DropdownMenu({
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Root>) {
  return <DropdownMenuPrimitive.Root data-slot="dropdown-menu" {...props} />
}

function DropdownMenuTrigger({
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Trigger>) {
  return (
    <DropdownMenuPrimitive.Trigger data-slot="dropdown-menu-trigger" {...props} />
  )
}

function DropdownMenuContent({
  className,
  sideOffset = 4,
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Content>) {
  return (
    <DropdownMenuPrimitive.Portal>
      <DropdownMenuPrimitive.Content
        data-slot="dropdown-menu-content"
        sideOffset={sideOffset}
        className={cn(
          'z-50 max-h-[min(320px,calc(100dvh-16px))] min-w-40 overflow-y-auto overflow-x-hidden rounded-[4px] border bg-popover p-1 text-popover-foreground shadow-overlay outline-none',
          className,
        )}
        {...props}
      />
    </DropdownMenuPrimitive.Portal>
  )
}

function DropdownMenuItem({
  className,
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Item>) {
  return (
    <DropdownMenuPrimitive.Item
      data-slot="dropdown-menu-item"
      className={cn(
        focusRing,
        // The item sits flush against its neighbours, so the outline is drawn
        // inside it. The background highlight stays for the pointer, but a
        // background is all forced-colors mode discards.
        'relative flex min-h-[22px] coarse:min-h-11 cursor-default items-center gap-2 rounded-[2px] px-2 py-0 text-[13px] leading-5 select-none data-[highlighted]:bg-selection data-[highlighted]:text-selection-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*="size-"])]:size-3.5',
        'focus-visible:-outline-offset-2',
        className,
      )}
      {...props}
    />
  )
}

export {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
}
