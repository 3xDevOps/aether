import type * as React from 'react'
import { cn } from '@/lib/utils'

export function SectionLabel({
  as: Tag = 'p',
  className,
  ...props
}: React.ComponentProps<'p'> & { as?: 'p' | 'h2' | 'h3' }) {
  return <Tag data-slot="section-label" className={cn('text-ui-sm font-medium text-muted', className)} {...props} />
}
