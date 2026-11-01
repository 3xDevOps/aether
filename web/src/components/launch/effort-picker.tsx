import type * as React from 'react'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

const agentDefault = '@default'

/** An effort level from `levels`: "" is the agent's own default, which travels
 * as `@default` because Radix Select reserves the empty value. */
export function EffortPicker({ levels, value, onChange, disabled = false, ...trigger }: {
  levels: string[]
  value: string
  onChange: (effort: string) => void
  disabled?: boolean
} & Omit<React.ComponentProps<typeof SelectTrigger>, 'value' | 'onChange'>) {
  return (
    <Select value={value || agentDefault} onValueChange={(next) => onChange(next === agentDefault ? '' : next)} disabled={disabled}>
      <SelectTrigger {...trigger}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={agentDefault}>Agent default</SelectItem>
        {value && !levels.includes(value) && <SelectItem value={value}>{value}</SelectItem>}
        {levels.map((level) => <SelectItem key={level} value={level}>{level}</SelectItem>)}
      </SelectContent>
    </Select>
  )
}
