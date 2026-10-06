import { useId } from 'react'
import { ListFilter } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { eventLabel, type EventType } from '@/lib/events'

const filterTypes: EventType[] = [
  'run.status',
  'run.input',
  'run.title',
  'run.agent',
  'run.diff',
  'workspace.timeline',
  'workspace.room_message',
  'run.controller',
  'workspace.approval',
  'workspace.presence',
  'workspace.budget',
  'run.cost',
  'run.overlap',
  'coord.message',
  'git.branch',
  'run.protected',
  'sync.conflict',
  'server.update',
  'member.changed',
]

export const agentMessages = '@messages'

export type Option = [value: string, label: string]

const kinds: Option[] = [
  ['', 'Every event'],
  [agentMessages, 'Agent messages'],
  ...filterTypes.map((type): Option => [type, eventLabel[type]]),
]

const none = '@none'

function FilterSelect({ label, value, onChange, options }: { label: string; value: string; onChange: (value: string) => void; options: Option[] }) {
  const id = useId()
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Select value={value || none} onValueChange={(next) => onChange(next === none ? '' : next)}>
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map(([option, name]) => (
            <SelectItem key={option || none} value={option || none}>
              {name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

export interface FilterField {
  label: string
  value: string
  onChange: (value: string) => void
  options: Option[]
}

export function ActivityFilter({
  workspace,
  kind,
  onKind,
  fields,
  onClear,
}: {
  workspace?: FilterField
  kind: string
  onKind: (kind: string) => void
  fields: FilterField[]
  onClear: () => void
}) {
  const narrowed = (kind ? 1 : 0) + fields.filter((field) => field.value).length
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="secondary" size="sm">
          <ListFilter />
          {narrowed > 0 ? `Filter · ${narrowed}` : 'Filter'}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" aria-label="Filter activity" className="flex w-[min(320px,calc(100vw-16px))] flex-col gap-3">
        {workspace && <FilterSelect {...workspace} />}
        <FilterSelect label="Show" value={kind} onChange={onKind} options={kinds} />
        {fields.map((field) => <FilterSelect key={field.label} {...field} />)}
        {narrowed > 0 && (
          <div className="flex justify-end">
            <Button variant="ghost" size="sm" onClick={onClear}>Clear filters</Button>
          </div>
        )}
      </PopoverContent>
    </Popover>
  )
}
