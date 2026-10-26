import type * as React from 'react'
import { cn } from '@/lib/utils'

const widths = { md: 'max-w-3xl', lg: 'max-w-4xl', xl: 'max-w-6xl' } as const

export function PageBody({
  width = 'md',
  className,
  ...props
}: React.ComponentProps<'div'> & { width?: keyof typeof widths }) {
  return (
    <div
      data-slot="page-body"
      className={cn(
        'mx-auto flex w-full min-w-0 animate-fade-in flex-col gap-8 px-4 py-6 motion-reduce:animate-none sm:px-6',
        widths[width],
        className,
      )}
      {...props}
    />
  )
}
