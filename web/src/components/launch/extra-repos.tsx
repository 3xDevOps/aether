import { Plus, X } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import type { Workspace } from '@/lib/types'

export function ExtraRepos({
  options,
  value,
  onChange,
  disabled,
}: {
  options: Workspace[]
  value: string[]
  onChange: (ids: string[]) => void
  disabled?: boolean
}) {
  const chosen = options.filter((workspace) => value.includes(workspace.id))
  const toggle = (id: string, on: boolean) => onChange(on ? [...value, id] : value.filter((item) => item !== id))
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-1.5">
        <Popover>
          <PopoverTrigger asChild>
            <Button type="button" variant="secondary" size="sm" disabled={disabled}>
              <Plus aria-hidden />
              Add repos
            </Button>
          </PopoverTrigger>
          <PopoverContent className="flex max-h-72 flex-col gap-0.5 p-1" aria-label="Add repos">
            {options.map((workspace) => (
              <label key={workspace.id} className="flex cursor-pointer items-center gap-2 rounded-control px-2 py-1.5 hover:bg-chrome coarse:min-h-11">
                <Checkbox checked={value.includes(workspace.id)} onCheckedChange={(on) => toggle(workspace.id, on === true)} />
                <span className="min-w-0 flex-1 truncate">{workspace.name}</span>
                <span className="font-code text-ui-sm text-muted">{workspace.base_branch}</span>
              </label>
            ))}
          </PopoverContent>
        </Popover>
        {chosen.map((workspace) => (
          <Badge key={workspace.id}>
            {workspace.name}
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              label={`Remove ${workspace.name}`}
              disabled={disabled}
              className="-mr-1 size-4"
              onClick={() => toggle(workspace.id, false)}
            >
              <X />
            </Button>
          </Badge>
        ))}
      </div>
      {chosen.length > 0 && (
        <p className="text-ui-sm text-muted">
          {'Each extra repo is checked out beside /workspace, at /repos/<name>, on its own run branch.'}
        </p>
      )}
    </div>
  )
}
