import { Slot } from 'radix-ui'
import * as React from 'react'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

export function FormField({
  label,
  help,
  error,
  children,
  className,
}: {
  label: React.ReactNode
  help?: React.ReactNode
  error?: React.ReactNode
  children: React.ReactElement
  className?: string
}) {
  const id = React.useId()
  const helpId = `${id}-help`
  const errorId = `${id}-error`
  const describedBy = [help && helpId, error && errorId].filter(Boolean).join(' ') || undefined
  return (
    <div data-slot="form-field" className={cn('flex flex-col gap-1', className)}>
      <Label htmlFor={id}>{label}</Label>
      <Slot.Root id={id} aria-describedby={describedBy} aria-invalid={error ? true : undefined}>
        {children}
      </Slot.Root>
      {help && (
        <p id={helpId} className="text-ui-sm text-muted">
          {help}
        </p>
      )}
      {error && (
        <p id={errorId} className="text-ui-sm text-state-failed">
          {error}
        </p>
      )}
    </div>
  )
}
