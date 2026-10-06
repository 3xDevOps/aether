import type * as React from 'react'
import { Toaster as Sonner } from 'sonner'
import { cn, surface } from '@/lib/utils'

export function Toaster(props: React.ComponentProps<typeof Sonner>) {
  return (
    <Sonner
      {...props}
      toastOptions={{
        className: cn(surface, 'px-3 py-2 text-ui'),
        classNames: { title: 'font-medium', description: 'text-muted' },
      }}
    />
  )
}
