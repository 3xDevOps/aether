import { AlertDialog as AlertDialogPrimitive } from 'radix-ui'
import type * as React from 'react'
import { buttonVariants } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { VariantProps } from 'class-variance-authority'

export function AlertDialog(
  props: React.ComponentProps<typeof AlertDialogPrimitive.Root>,
) {
  return <AlertDialogPrimitive.Root data-slot="alert-dialog" {...props} />
}

export function AlertDialogContent({
  className,
  ...props
}: React.ComponentProps<typeof AlertDialogPrimitive.Content>) {
  return (
    <AlertDialogPrimitive.Portal>
      <AlertDialogPrimitive.Overlay
        data-slot="alert-dialog-overlay"
        className="fixed inset-0 z-50 bg-scrim data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0 motion-reduce:animate-none"
      />
      <AlertDialogPrimitive.Content
        data-slot="alert-dialog-content"
        className={cn(
          'fixed top-4 left-[50%] z-50 grid max-h-[calc(100dvh-2rem)] w-full max-w-[calc(100%-2rem)] translate-x-[-50%] translate-y-0 gap-3 overflow-y-auto rounded-[4px] border bg-popover p-4 text-popover-foreground duration-150 outline-none data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[state=open]:animate-in data-[state=open]:fade-in-0 motion-reduce:animate-none sm:top-[50%] sm:max-w-lg sm:translate-y-[-50%]',
          className,
        )}
        {...props}
      />
    </AlertDialogPrimitive.Portal>
  )
}

// A head and foot of its own rather than the dialog's: Radix gives an alert
// dialog its own context scope, so anything reaching for Dialog's - which is
// what `DialogFooter`'s close button does - throws here.
export function AlertDialogHeader({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="alert-dialog-header"
      className={cn('flex flex-col gap-1 text-left', className)}
      {...props}
    />
  )
}

export function AlertDialogFooter({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="alert-dialog-footer"
      className={cn('flex flex-col-reverse gap-2 sm:flex-row sm:justify-end', className)}
      {...props}
    />
  )
}

export function AlertDialogTitle({
  className,
  ...props
}: React.ComponentProps<typeof AlertDialogPrimitive.Title>) {
  return (
    <AlertDialogPrimitive.Title
      data-slot="alert-dialog-title"
      className={cn('text-base font-semibold leading-5 tracking-tight', className)}
      {...props}
    />
  )
}

export function AlertDialogDescription({
  className,
  ...props
}: React.ComponentProps<typeof AlertDialogPrimitive.Description>) {
  return (
    <AlertDialogPrimitive.Description
      data-slot="alert-dialog-description"
      className={cn('text-[13px] leading-5 text-muted-foreground', className)}
      {...props}
    />
  )
}

/** The verb the dialog exists to confirm; destructive unless the caller says
 * otherwise, which most of them are. */
export function AlertDialogAction({
  className,
  variant = 'destructive',
  size,
  ...props
}: React.ComponentProps<typeof AlertDialogPrimitive.Action> &
  VariantProps<typeof buttonVariants>) {
  return (
    <AlertDialogPrimitive.Action
      data-slot="alert-dialog-action"
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  )
}

export function AlertDialogCancel({
  className,
  ...props
}: React.ComponentProps<typeof AlertDialogPrimitive.Cancel>) {
  return (
    <AlertDialogPrimitive.Cancel
      data-slot="alert-dialog-cancel"
      className={cn(buttonVariants({ variant: 'outline' }), className)}
      {...props}
    />
  )
}
