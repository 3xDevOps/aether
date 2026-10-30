import { RadioGroup as RadioGroupPrimitive } from 'radix-ui'
import { useId } from 'react'
import { Badge } from '@/components/ui/badge'
import { Label } from '@/components/ui/label'
import type { Permission } from '@/lib/types'
import { cn, focusRingInset } from '@/lib/utils'

export const permissionFeature = 'permission'

export const permissions: { value: Permission; label: string; description: string; detail: string }[] = [
  {
    value: 'root',
    label: 'Root access',
    description: 'sudo runs without asking',
    detail: 'The agent can install system packages and change anything in its container, as well as your home.',
  },
  {
    value: 'default',
    label: 'Default',
    description: 'Asks you before each sudo',
    detail: 'The agent runs unprivileged. Each sudo command waits until you allow it. It can still change your home.',
  },
  {
    value: 'isolated',
    label: 'Isolated',
    description: 'Only the checkout is kept',
    detail: 'The agent works on a throwaway copy of your home, with no sudo or Docker, and reaches only its own service. Nothing it changes outside the checkout outlives the run.',
  },
]

export function permissionLabel(permission: Permission): string {
  return permissions.find((p) => p.value === permission)?.label ?? permission
}

export function PermissionControl({ value, onChange, disabled = false }: { value: Permission; onChange: (permission: Permission) => void; disabled?: boolean }) {
  const id = useId()
  const detail = permissions.find((p) => p.value === value)?.detail
  return (
    <div className="flex flex-col gap-1">
      <Label asChild>
        <p id={`${id}-label`}>Permission</p>
      </Label>
      <RadioGroupPrimitive.Root
        aria-labelledby={`${id}-label`}
        aria-describedby={`${id}-detail`}
        value={value}
        onValueChange={(next) => onChange(next as Permission)}
        disabled={disabled}
        className="grid gap-0.5 rounded-control bg-chrome p-0.5 sm:grid-cols-3"
      >
        {permissions.map((permission) => (
          <RadioGroupPrimitive.Item
            key={permission.value}
            value={permission.value}
            aria-labelledby={`${id}-${permission.value}`}
            aria-describedby={`${id}-${permission.value}-description`}
            className={cn(
              focusRingInset,
              'flex min-w-0 cursor-pointer flex-col items-start gap-0.5 rounded-control px-2 py-1.5 text-left text-muted transition-colors duration-100 motion-reduce:transition-none',
              'hover:enabled:bg-hover-chrome hover:enabled:text-text data-[state=checked]:bg-raised data-[state=checked]:text-text data-[state=checked]:ring-1 data-[state=checked]:ring-seam',
              permission.value === 'root' && 'data-[state=checked]:ring-state-needs-you',
              'disabled:cursor-not-allowed disabled:opacity-50',
            )}
          >
            <span className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5">
              <span id={`${id}-${permission.value}`} className="text-ui font-medium">
                {permission.label}
              </span>
              {permission.value === 'default' && <Badge tone="review">Recommended</Badge>}
            </span>
            <span id={`${id}-${permission.value}-description`} className="text-ui-sm text-muted">
              {permission.description}
            </span>
          </RadioGroupPrimitive.Item>
        ))}
      </RadioGroupPrimitive.Root>
      <p id={`${id}-detail`} className="text-ui-sm text-muted">
        {detail}
      </p>
    </div>
  )
}
