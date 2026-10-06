import { Dialog as DialogPrimitive } from 'radix-ui'
import type * as React from 'react'
import { X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { cn, surface } from '@/lib/utils'

function Dialog(props: React.ComponentProps<typeof DialogPrimitive.Root>) {
  return <DialogPrimitive.Root data-slot="dialog" {...props} />
}

function DialogTrigger(props: React.ComponentProps<typeof DialogPrimitive.Trigger>) {
  return <DialogPrimitive.Trigger data-slot="dialog-trigger" {...props} />
}

function DialogClose(props: React.ComponentProps<typeof DialogPrimitive.Close>) {
  return <DialogPrimitive.Close data-slot="dialog-close" {...props} />
}

function DialogPortal(props: React.ComponentProps<typeof DialogPrimitive.Portal>) {
  return <DialogPrimitive.Portal data-slot="dialog-portal" {...props} />
}

export const overlayClass = 'fixed inset-0 z-50 bg-scrim animate-fade-in motion-reduce:animate-none'

function DialogOverlay({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Overlay>) {
  return <DialogPrimitive.Overlay data-slot="dialog-overlay" className={cn(overlayClass, className)} {...props} />
}

const bottomSheet = 'inset-x-0 bottom-[var(--keyboard-inset,0px)] max-h-[calc(85dvh-var(--keyboard-inset,0px))] rounded-b-none border-x-0 border-b-0 pb-[calc(1rem+env(safe-area-inset-bottom))] animate-sheet-up'

export type DialogVariant = 'center' | 'bottom' | 'side'

export function dialogContentClass(variant: DialogVariant = 'center', side: 'left' | 'right' = 'right') {
  return cn(
    surface,
    'fixed z-50 outline-none motion-reduce:animate-none',
    variant !== 'side' && 'grid gap-3 overflow-y-auto p-4',
    variant === 'center' &&
      'top-1/2 left-1/2 max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 animate-overlay-in',
    variant === 'center' &&
      'max-md:top-auto max-md:left-0 max-md:w-full max-md:max-w-none max-md:translate-x-0 max-md:translate-y-0 max-md:max-h-[calc(85dvh-var(--keyboard-inset,0px))] max-md:inset-x-0 max-md:bottom-[var(--keyboard-inset,0px)] max-md:rounded-b-none max-md:border-x-0 max-md:border-b-0 max-md:pb-[calc(1rem+env(safe-area-inset-bottom))] max-md:animate-sheet-up',
    variant === 'bottom' && bottomSheet,
    variant === 'side' && 'inset-y-0 flex max-w-full flex-col overflow-hidden border-y-0 pt-[var(--safe-top)] pb-[env(safe-area-inset-bottom)]',
    variant === 'side' && side === 'left' && 'left-0 rounded-l-none border-l-0 pl-[env(safe-area-inset-left)] animate-sheet-left',
    variant === 'side' && side === 'right' && 'right-0 rounded-r-none border-r-0 pr-[env(safe-area-inset-right)] animate-sheet-right',
  )
}

function DialogContent({
  className,
  children,
  variant = 'center',
  side = 'right',
  showCloseButton = true,
  overlayClassName,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Content> & {
  variant?: DialogVariant
  side?: 'left' | 'right'
  showCloseButton?: boolean
  overlayClassName?: string
}) {
  return (
    <DialogPortal>
      <DialogOverlay className={overlayClassName} />
      <DialogPrimitive.Content
        data-slot="dialog-content"
        data-variant={variant}
        className={cn(dialogContentClass(variant, side), className)}
        {...props}
      >
        {children}
        {showCloseButton && (
          <DialogPrimitive.Close asChild>
            <Button variant="ghost" size="icon-sm" label="Close" className="absolute top-2 right-2">
              <X />
            </Button>
          </DialogPrimitive.Close>
        )}
      </DialogPrimitive.Content>
    </DialogPortal>
  )
}

function DialogHeader({ className, ...props }: React.ComponentProps<'div'>) {
  return <div data-slot="dialog-header" className={cn('flex flex-col gap-1 pr-6 text-left', className)} {...props} />
}

export const dialogFooterClass = 'flex flex-col-reverse gap-2 sm:flex-row sm:justify-end'

function DialogFooter({
  className,
  showCloseButton = false,
  children,
  ...props
}: React.ComponentProps<'div'> & { showCloseButton?: boolean }) {
  return (
    <div data-slot="dialog-footer" className={cn(dialogFooterClass, className)} {...props}>
      {children}
      {showCloseButton && (
        <DialogPrimitive.Close asChild>
          <Button variant="secondary">Close</Button>
        </DialogPrimitive.Close>
      )}
    </div>
  )
}

export const dialogTitleClass = 'text-title text-text'
export const dialogDescriptionClass = 'text-ui text-muted'

function DialogTitle({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Title>) {
  return <DialogPrimitive.Title data-slot="dialog-title" className={cn(dialogTitleClass, className)} {...props} />
}

function DialogDescription({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Description>) {
  return (
    <DialogPrimitive.Description
      data-slot="dialog-description"
      className={cn(dialogDescriptionClass, className)}
      {...props}
    />
  )
}

export {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogOverlay,
  DialogPortal,
  DialogTitle,
  DialogTrigger,
}
