import {
  Chip as HeroChip,
  Tooltip as HeroTooltip,
} from '@heroui/react'
import type * as React from 'react'
import { cn } from '@/lib/utils'

type ChipProps = React.ComponentProps<typeof HeroChip>

const ChipRoot = ({ className, ...props }: ChipProps) => (
  <HeroChip
    className={cn(
      'aether-chip !h-[20px] !min-h-[20px] !rounded-[2px] !px-1.5 text-[12px] leading-4',
      className,
    )}
    {...props}
  />
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

const TooltipContent = ({
  className,
  ...props
}: React.ComponentProps<typeof HeroTooltip.Content>) => (
  <HeroTooltip.Content
    className={cn(
      'aether-tooltip max-w-xs rounded-[4px] border border-border bg-popover px-2 py-1 text-[12px] leading-4 text-popover-foreground shadow-overlay',
      className,
    )}
    {...props}
  />
)

/** HeroUI v3 accessible hover and keyboard tooltip using shared overlay tokens. */
export const Tooltip = Object.assign(TooltipRoot, {
  Root: TooltipRoot,
  Trigger: HeroTooltip.Trigger,
  Content: TooltipContent,
  Arrow: HeroTooltip.Arrow,
})
