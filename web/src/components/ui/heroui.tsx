import {
  Chip as HeroChip,
  Tabs as HeroTabs,
  Tooltip as HeroTooltip,
} from '@heroui/react'
import type * as React from 'react'
import { cn } from '@/lib/utils'

type TabsProps = React.ComponentProps<typeof HeroTabs>

const TabsRoot = ({ className, ...props }: TabsProps) => (
  <HeroTabs className={cn('aether-tabs', className)} {...props} />
)

/**
 * HeroUI v3 tabs, tuned to the workbench's compact neutral treatment.
 *
 * Usage:
 * <Tabs selectedKey={active} onSelectionChange={setActive}>
 *   <Tabs.List aria-label="Run views">
 *     <Tabs.Tab id="overview">Overview</Tabs.Tab>
 *   </Tabs.List>
 *   <Tabs.Panel id="overview">...</Tabs.Panel>
 * </Tabs>
 */
export const Tabs = Object.assign(TabsRoot, {
  Root: TabsRoot,
  ListContainer: HeroTabs.ListContainer,
  List: HeroTabs.List,
  Tab: HeroTabs.Tab,
  Indicator: HeroTabs.Indicator,
  Separator: HeroTabs.Separator,
  Panel: HeroTabs.Panel,
})

type ChipProps = React.ComponentProps<typeof HeroChip>

const ChipRoot = ({ className, ...props }: ChipProps) => (
  <HeroChip className={cn('aether-chip', className)} {...props} />
)

/** HeroUI v3 status or metadata chip using the shared token palette. */
export const Chip = Object.assign(ChipRoot, {
  Root: ChipRoot,
  Label: HeroChip.Label,
})

type TooltipProps = React.ComponentProps<typeof HeroTooltip>

/**
 * `--tooltip-delay` lives in HeroUI's full theme stylesheet, which this app
 * does not import, so without a delay here React Aria falls back to its own
 * 1.5s - about three times what a native `title` took, on the controls this
 * dashboard puts its hints on. Focus is unaffected either way; it opens at
 * once.
 */
const TooltipRoot = ({ delay = 300, ...props }: TooltipProps) => (
  <HeroTooltip delay={delay} {...props} />
)

const TooltipContent = ({ className, ...props }: React.ComponentProps<typeof HeroTooltip.Content>) => (
  <HeroTooltip.Content className={cn('aether-tooltip', className)} {...props} />
)

/** HeroUI v3 accessible hover and keyboard tooltip using shared overlay tokens. */
export const Tooltip = Object.assign(TooltipRoot, {
  Root: TooltipRoot,
  Trigger: HeroTooltip.Trigger,
  Content: TooltipContent,
  Arrow: HeroTooltip.Arrow,
})
