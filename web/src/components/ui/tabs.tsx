import { Tabs as TabsPrimitive } from 'radix-ui'
import type * as React from 'react'
import { cn, focusRingInset } from '@/lib/utils'

export function Tabs({
  activationMode = 'manual',
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Root>) {
  return <TabsPrimitive.Root data-slot="tabs" activationMode={activationMode} {...props} />
}

export function TabsList({
  look = 'underline',
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.List> & { look?: 'underline' | 'segmented' }) {
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      data-look={look}
      className={cn(
        'group/tabs flex min-w-0 items-center',
        look === 'underline' ? 'h-8 gap-3 coarse:h-11' : 'h-7 gap-0.5 rounded-control bg-chrome p-0.5 coarse:h-11',
        className,
      )}
      {...props}
    />
  )
}

export function TabsTrigger({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      data-slot="tabs-trigger"
      className={cn(
        focusRingInset,
        'inline-flex h-full shrink-0 items-center gap-1.5 text-ui font-medium whitespace-nowrap text-muted transition-colors duration-100 motion-reduce:transition-none',
        'hover:text-text disabled:pointer-events-none disabled:opacity-50 data-[state=active]:text-text [&_svg]:size-3.5 [&_svg]:shrink-0',
        'group-data-[look=underline]/tabs:border-b-2 group-data-[look=underline]/tabs:border-transparent group-data-[look=underline]/tabs:px-0.5 group-data-[look=underline]/tabs:data-[state=active]:border-accent',
        'group-data-[look=segmented]/tabs:rounded-control group-data-[look=segmented]/tabs:px-2 group-data-[look=segmented]/tabs:data-[state=active]:bg-raised group-data-[look=segmented]/tabs:data-[state=active]:ring-1 group-data-[look=segmented]/tabs:data-[state=active]:ring-seam',
        className,
      )}
      {...props}
    />
  )
}

export function TabsContent({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content data-slot="tabs-content" className={cn('min-h-0 outline-none', className)} {...props} />
}
