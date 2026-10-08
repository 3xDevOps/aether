"use client"

import * as React from "react"
import { Command as CommandPrimitive } from "cmdk"
import { Search } from '@/components/icons'

import { cn, focusRing } from "@/lib/utils"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"

function Command({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive>) {
  return (
    <CommandPrimitive
      data-slot="command"
      className={cn(
        'flex min-h-0 h-auto w-full flex-col overflow-hidden rounded-[4px] bg-raised text-text',
        className,
      )}
      {...props}
    />
  )
}

function CommandDialog({
  title = "Command Palette",
  description = "Search for a command to run…",
  children,
  className,
  showCloseButton = true,
  onOpenAutoFocus,
  onCloseAutoFocus,
  ...props
}: React.ComponentProps<typeof Dialog> & {
  title?: string
  description?: string
  className?: string
  showCloseButton?: boolean
  onOpenAutoFocus?: React.ComponentProps<typeof DialogContent>['onOpenAutoFocus']
  onCloseAutoFocus?: React.ComponentProps<typeof DialogContent>['onCloseAutoFocus']
}) {
  return (
    <Dialog {...props}>
      <DialogContent
        overlayClassName="!bg-transparent"
        onOpenAutoFocus={(event) => {
          onOpenAutoFocus?.(event)
          if (event.defaultPrevented) return
          // Skip Radix's walk through every option; its focus trap stays active.
          const input = (event.target as HTMLElement).querySelector<HTMLInputElement>('[cmdk-input]')
          if (input) {
            input.focus({ preventScroll: true })
            if (document.activeElement === input) event.preventDefault()
          }
        }}
        onCloseAutoFocus={onCloseAutoFocus}
        className={cn(
          'top-[calc(var(--app-header-bottom,var(--top-bar-height))_+_8px)] min-h-0 max-h-[calc(100dvh_-_var(--app-header-bottom,var(--top-bar-height))_-_16px)] max-w-[min(600px,calc(100%-1rem))] translate-y-0 grid-rows-[auto_auto] gap-0 overflow-hidden border-seam/90 bg-raised p-0 shadow-overlay data-[state=closed]:animate-none data-[state=open]:animate-none sm:top-[calc(var(--app-header-bottom,var(--top-bar-height))_+_8px)] sm:max-w-[min(600px,calc(100%-1rem))] sm:translate-y-0',
          className,
        )}
        showCloseButton={showCloseButton}
      >
        {/* Radix needs the title inside the content, not beside it. */}
        <DialogHeader className="sr-only">
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <Command className="[&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted [&_[cmdk-group]]:px-1 [&_[cmdk-group]:not([hidden])_~[cmdk-group]]:pt-0 [&_[cmdk-input-wrapper]_svg]:size-4 [&_[cmdk-item]_svg]:size-4">
          {children}
        </Command>
      </DialogContent>
    </Dialog>
  )
}

function CommandInput({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Input>) {
  return (
    <div
      data-slot="command-input-wrapper"
      className="flex h-[var(--top-bar-height)] items-center gap-2 border-b px-2"
    >
      <Search className="size-4 shrink-0 text-muted" />
      <CommandPrimitive.Input
        data-slot="command-input"
        className={cn(
          focusRing,
          'flex h-[26px] w-full rounded-[2px] bg-transparent px-1 text-ui leading-6 placeholder:text-muted coarse:h-10 disabled:cursor-not-allowed disabled:opacity-50',
          className,
        )}
        {...props}
      />
    </div>
  )
}

function CommandList({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.List>) {
  return (
    <CommandPrimitive.List
      data-slot="command-list"
      className={cn(
        'max-h-[min(520px,calc(100dvh_-_2_*_var(--top-bar-height)_-_8px))] scroll-py-1 overflow-x-hidden overflow-y-auto',
        className,
      )}
      {...props}
    />
  )
}

function CommandEmpty({
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Empty>) {
  return (
    <CommandPrimitive.Empty
      data-slot="command-empty"
      className="py-4 text-center text-ui text-muted"
      {...props}
    />
  )
}

function CommandGroup({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Group>) {
  return (
    <CommandPrimitive.Group
      data-slot="command-group"
      className={cn(
        'overflow-hidden p-1 text-text [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1 [&_[cmdk-group-heading]]:text-ui-sm [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted',
        className,
      )}
      {...props}
    />
  )
}

function CommandSeparator({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Separator>) {
  return (
    <CommandPrimitive.Separator
      data-slot="command-separator"
      className={cn('-mx-1 h-px bg-seam', className)}
      {...props}
      aria-hidden="true"
    />
  )
}

function CommandItem({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Item>) {
  return (
    <CommandPrimitive.Item
      data-slot="command-item"
      className={cn(
        'relative flex min-h-[22px] coarse:min-h-11 cursor-default items-center gap-2 rounded-[2px] px-2 py-0 text-ui leading-5 outline-hidden select-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50 data-[selected=true]:bg-selection data-[selected=true]:text-text [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*="size-"])]:size-4 [&_svg:not([class*="text-"])]:text-muted',
        className,
      )}
      {...props}
    />
  )
}

function CommandShortcut({
  className,
  ...props
}: React.ComponentProps<"span">) {
  return (
    <span
      data-slot="command-shortcut"
      className={cn(
        'ml-auto text-ui-sm tracking-wide text-muted',
        className,
      )}
      {...props}
    />
  )
}

export {
  Command,
  CommandDialog,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
  CommandShortcut,
  CommandSeparator,
}
