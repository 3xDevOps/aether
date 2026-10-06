import { DropdownMenu as MenuPrimitive } from 'radix-ui'
import * as React from 'react'
import { Check, ChevronRight } from '@/components/icons'
import { useIsMobile } from '@/lib/breakpoints'
import { cn, focusRingInset, surface } from '@/lib/utils'

export function Menu(props: React.ComponentProps<typeof MenuPrimitive.Root>) {
  return <MenuPrimitive.Root data-slot="menu" {...props} />
}

export function MenuTrigger(props: React.ComponentProps<typeof MenuPrimitive.Trigger>) {
  return <MenuPrimitive.Trigger data-slot="menu-trigger" {...props} />
}

const sheetAbove = 6

export function MenuContent({
  className,
  sideOffset = 4,
  children,
  ...props
}: React.ComponentProps<typeof MenuPrimitive.Content>) {
  const [items, setItems] = React.useState(0)
  const countItems = React.useCallback((node: HTMLDivElement | null) => {
    if (node) setItems(node.querySelectorAll('[role^="menuitem"]').length)
  }, [])
  const sheet = useIsMobile() && items > sheetAbove
  return (
    <>
      {/* Radix's portal takes exactly one child. */}
      {sheet && (
        <MenuPrimitive.Portal>
          <div aria-hidden className="fixed inset-0 z-50 animate-fade-in bg-scrim motion-reduce:animate-none" />
        </MenuPrimitive.Portal>
      )}
      <MenuPrimitive.Portal>
        <MenuPrimitive.Content
          ref={countItems}
          data-slot="menu-content"
          data-sheet={sheet || undefined}
          sideOffset={sideOffset}
          collisionPadding={8}
          className={cn(
            surface,
            'z-50 min-w-40 overflow-x-hidden overflow-y-auto p-1 outline-none motion-reduce:animate-none',
            sheet
              ? 'max-h-[85dvh] w-screen rounded-b-none border-x-0 border-b-0 pb-[calc(0.25rem+env(safe-area-inset-bottom))] animate-sheet-up'
              : 'max-h-[min(320px,var(--radix-dropdown-menu-content-available-height))] animate-overlay-in',
            className,
          )}
          {...props}
        >
          {children}
        </MenuPrimitive.Content>
      </MenuPrimitive.Portal>
    </>
  )
}

const itemClass = cn(
  focusRingInset,
  'relative flex h-7 cursor-default items-center gap-2 rounded-control px-2 text-ui text-text select-none coarse:h-11',
  'data-[highlighted]:bg-hover-chrome data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
  '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*="size-"])]:size-3.5 [&_svg]:text-muted',
)

export function MenuItem({
  className,
  tone,
  ...props
}: React.ComponentProps<typeof MenuPrimitive.Item> & { tone?: 'danger' }) {
  return (
    <MenuPrimitive.Item
      data-slot="menu-item"
      className={cn(itemClass, tone === 'danger' && 'text-state-failed [&_svg]:text-state-failed', className)}
      {...props}
    />
  )
}

export function MenuCheckboxItem({
  className,
  children,
  ...props
}: React.ComponentProps<typeof MenuPrimitive.CheckboxItem>) {
  return (
    <MenuPrimitive.CheckboxItem data-slot="menu-checkbox-item" className={cn(itemClass, 'pl-7', className)} {...props}>
      <MenuPrimitive.ItemIndicator className="absolute left-2 flex items-center">
        <Check />
      </MenuPrimitive.ItemIndicator>
      {children}
    </MenuPrimitive.CheckboxItem>
  )
}

export function MenuLabel({ className, ...props }: React.ComponentProps<typeof MenuPrimitive.Label>) {
  return (
    <MenuPrimitive.Label
      data-slot="menu-label"
      className={cn('px-2 pt-1.5 pb-1 text-ui-sm font-medium text-muted', className)}
      {...props}
    />
  )
}

export function MenuSeparator({ className, ...props }: React.ComponentProps<typeof MenuPrimitive.Separator>) {
  return (
    <MenuPrimitive.Separator data-slot="menu-separator" className={cn('-mx-1 my-1 h-px bg-seam', className)} {...props} />
  )
}

export function MenuRadioGroup(props: React.ComponentProps<typeof MenuPrimitive.RadioGroup>) {
  return <MenuPrimitive.RadioGroup data-slot="menu-radio-group" {...props} />
}

export function MenuRadioItem({
  className,
  children,
  ...props
}: React.ComponentProps<typeof MenuPrimitive.RadioItem>) {
  return (
    <MenuPrimitive.RadioItem data-slot="menu-radio-item" className={cn(itemClass, 'pl-7', className)} {...props}>
      <MenuPrimitive.ItemIndicator className="absolute left-2 flex items-center">
        <Check />
      </MenuPrimitive.ItemIndicator>
      {children}
    </MenuPrimitive.RadioItem>
  )
}

export function MenuSub(props: React.ComponentProps<typeof MenuPrimitive.Sub>) {
  return <MenuPrimitive.Sub data-slot="menu-sub" {...props} />
}

export function MenuSubTrigger({
  className,
  children,
  ...props
}: React.ComponentProps<typeof MenuPrimitive.SubTrigger>) {
  return (
    <MenuPrimitive.SubTrigger
      data-slot="menu-sub-trigger"
      className={cn(itemClass, 'data-[state=open]:bg-hover-chrome', className)}
      {...props}
    >
      {children}
      <ChevronRight className="ml-auto" />
    </MenuPrimitive.SubTrigger>
  )
}

export function MenuSubContent({ className, ...props }: React.ComponentProps<typeof MenuPrimitive.SubContent>) {
  return (
    <MenuPrimitive.Portal>
      <MenuPrimitive.SubContent
        data-slot="menu-sub-content"
        sideOffset={4}
        collisionPadding={8}
        className={cn(surface, 'z-50 min-w-32 p-1 outline-none animate-overlay-in motion-reduce:animate-none', className)}
        {...props}
      />
    </MenuPrimitive.Portal>
  )
}
