import { Tabs as TabsPrimitive } from 'radix-ui'
import * as React from 'react'
import { SlidingIndicator } from '@/components/ui/sliding-indicator'
import { cn, focusRingInset } from '@/lib/utils'

// Radix keeps the active value to itself; the list's indicator needs it.
const ActiveTab = React.createContext<string | undefined>(undefined)

export function Tabs({
  activationMode = 'manual',
  value,
  defaultValue,
  onValueChange,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Root>) {
  const [chosen, setChosen] = React.useState(defaultValue)
  return (
    <ActiveTab.Provider value={value ?? chosen}>
      <TabsPrimitive.Root
        data-slot="tabs"
        activationMode={activationMode}
        value={value}
        defaultValue={defaultValue}
        onValueChange={(next) => {
          setChosen(next)
          onValueChange?.(next)
        }}
        {...props}
      />
    </ActiveTab.Provider>
  )
}

export function TabsList({
  look = 'underline',
  className,
  children,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.List> & { look?: 'underline' | 'segmented' }) {
  const active = React.useContext(ActiveTab)
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      data-look={look}
      className={cn(
        'group/tabs relative flex min-w-0 items-center',
        look === 'underline' ? 'h-8 gap-3 coarse:h-11' : 'h-7 gap-0.5 rounded-control bg-chrome p-0.75 coarse:h-11',
        className,
      )}
      {...props}
    >
      <SlidingIndicator
        activeKey={active}
        className={
          look === 'underline'
            ? 'after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:rounded-full after:bg-accent'
            : 'rounded-chip bg-raised ring-1 ring-seam'
        }
      />
      {children}
    </TabsPrimitive.List>
  )
}

export function TabsTrigger({ className, value, ...props }: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      data-slot="tabs-trigger"
      data-indicator-key={value}
      value={value}
      className={cn(
        focusRingInset,
        'relative inline-flex h-full shrink-0 items-center gap-1.5 text-ui font-medium whitespace-nowrap text-muted transition-colors duration-100 motion-reduce:transition-none',
        'hover:text-text disabled:pointer-events-none disabled:opacity-50 data-[state=active]:text-text [&_svg]:size-3.5 [&_svg]:shrink-0',
        'group-data-[look=underline]/tabs:border-b-2 group-data-[look=underline]/tabs:border-transparent group-data-[look=underline]/tabs:px-0.5',
        'group-data-[look=segmented]/tabs:rounded-chip group-data-[look=segmented]/tabs:px-2',
        className,
      )}
      {...props}
    />
  )
}

export function TabsContent({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content data-slot="tabs-content" className={cn('min-h-0 outline-none', className)} {...props} />
}
