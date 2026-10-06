import { RadioGroup as RadioGroupPrimitive } from 'radix-ui'
import { type ReactNode, useId } from 'react'
import { modes, type Refusal } from '@/components/launch/modes'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import type { LaunchMode } from '@/lib/types'
import { cn, focusRingInset } from '@/lib/utils'

export function ModeControl({
  label,
  value,
  onChange,
  refused = {},
  help,
  onSetUp,
}: {
  label: string
  value: LaunchMode
  onChange: (mode: LaunchMode) => void
  refused?: Partial<Record<LaunchMode, Refusal>>
  help?: ReactNode
  onSetUp: () => void
}) {
  const id = useId()
  const reasons = modes.flatMap(({ value: mode }) => (refused[mode] ? [{ mode, ...refused[mode] }] : []))
  return (
    <div className="flex flex-col gap-1">
      <Label asChild>
        <p>{label}</p>
      </Label>
      <RadioGroupPrimitive.Root
        aria-label={label}
        value={value}
        onValueChange={(next) => onChange(next as LaunchMode)}
        className="grid gap-0.5 rounded-control bg-chrome p-0.5 sm:grid-cols-3"
      >
        {modes.map((mode) => (
          <RadioGroupPrimitive.Item
            key={mode.value}
            value={mode.value}
            disabled={Boolean(refused[mode.value])}
            aria-labelledby={`${id}-${mode.value}`}
            aria-describedby={`${id}-${mode.value}-description`}
            className={cn(
              focusRingInset,
              'group flex min-w-0 cursor-pointer flex-col items-start gap-0.5 rounded-control px-2 py-1.5 text-left text-muted transition-colors duration-100 motion-reduce:transition-none',
              'hover:enabled:bg-hover-chrome hover:enabled:text-text data-[state=checked]:bg-raised data-[state=checked]:text-text data-[state=checked]:ring-1 data-[state=checked]:ring-seam',
              'disabled:cursor-not-allowed disabled:text-icon-faint',
            )}
          >
            <span id={`${id}-${mode.value}`} className="text-ui font-medium">
              {mode.label}
            </span>
            <span id={`${id}-${mode.value}-description`} className="text-ui-sm text-muted group-disabled:text-icon-faint">
              {mode.description}
            </span>
          </RadioGroupPrimitive.Item>
        ))}
      </RadioGroupPrimitive.Root>
      {reasons.map(({ mode, reason, setUp }) => (
        <p key={mode} className="text-ui-sm text-muted">
          {reason}
          {setUp && (
            <>
              {' '}
              <Button type="button" variant="link" size="sm" onClick={onSetUp}>
                Set up
              </Button>
            </>
          )}
        </p>
      ))}
      {help && <p className="text-ui-sm text-muted">{help}</p>}
    </div>
  )
}
