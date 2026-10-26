import { useId, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react'
import { modeLabel } from '@/components/launch/modes'
import { Clock, Search } from '@/components/icons'
import { matchesSearch } from '@/components/templates/placeholders'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { SlidingIndicator } from '@/components/ui/sliding-indicator'
import type { Schedule, Template } from '@/lib/types'
import { cn, focusRingInset } from '@/lib/utils'

const moves: Record<string, (i: number, last: number) => number> = {
  ArrowDown: (i, last) => Math.min(i + 1, last),
  ArrowUp: (i) => Math.max(i - 1, 0),
  Home: () => 0,
  End: (_, last) => last,
}

/** For an overlay's onEscapeKeyDown: Escape in a non-empty template search clears it rather than closing the overlay. */
export function keepEscapeForSearch(event: globalThis.KeyboardEvent) {
  const target = event.target
  if (target instanceof HTMLInputElement && target.dataset.templateSearch !== undefined && target.value) event.preventDefault()
}

/**
 * The searchable listbox of a workspace's templates, sorted as given. The arrow keys, Home and End move the
 * selection and focus together; ArrowDown in the search enters the list.
 */
export function TemplateList({
  templates,
  schedules,
  agentNames,
  selected,
  onSelect,
  autoFocus,
  raised,
  className,
}: {
  templates: Template[]
  schedules: Schedule[]
  agentNames: Record<string, string>
  selected: string | undefined
  onSelect: (name: string) => void
  autoFocus?: boolean
  /** On a raised surface, such as a popover or dialog, rows hover with the chrome tone. */
  raised?: boolean
  className?: string
}) {
  const [query, setQuery] = useState('')
  const listID = useId()
  const scheduledID = useId()
  const options = useRef(new Map<string, HTMLElement>())
  const visible = templates.filter((t) => matchesSearch(t, agentNames[t.harness] ?? t.harness, query))
  const focusable = visible.some((t) => t.name === selected) ? selected : visible[0]?.name
  useLayoutEffect(() => {
    if (selected) options.current.get(selected)?.scrollIntoView({ block: 'nearest' })
  }, [selected])
  const moveTo = (name: string) => {
    onSelect(name)
    options.current.get(name)?.focus()
  }
  const onOptionKey = (event: KeyboardEvent, name: string) => {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      onSelect(name)
      return
    }
    const move = moves[event.key]
    if (!move) return
    event.preventDefault()
    const target = visible[move(visible.findIndex((t) => t.name === name), visible.length - 1)]
    if (target) moveTo(target.name)
  }
  return (
    <div className={cn('flex min-h-0 flex-col gap-2', className)}>
      <div className="relative shrink-0">
        <Search aria-hidden className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted" />
        <Input
          type="search"
          data-template-search=""
          autoFocus={autoFocus}
          aria-label="Search templates"
          aria-controls={listID}
          placeholder="Search templates"
          value={query}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape' && query) {
              e.preventDefault()
              setQuery('')
            } else if (e.key === 'ArrowDown' && focusable) {
              e.preventDefault()
              moveTo(focusable)
            }
          }}
          className="pr-14 pl-7 [&::-webkit-search-cancel-button]:hidden"
        />
        <span className="pointer-events-none absolute top-1/2 right-2 -translate-y-1/2 text-ui-sm text-muted tabular-nums">
          {query.trim() ? `${visible.length} of ${templates.length}` : templates.length}
        </span>
      </div>
      <span id={scheduledID} hidden>Scheduled</span>
      <div id={listID} role="listbox" aria-label="Templates" className="relative -mx-1 flex min-h-0 flex-col gap-0.5 overflow-y-auto px-1 pb-1">
        <SlidingIndicator activeKey={visible.some((t) => t.name === selected) ? selected : undefined} className="rounded-control bg-hover-chrome" />
        {visible.length === 0 && <p className="flex h-8 shrink-0 items-center px-2 text-ui text-muted">No templates match</p>}
        {visible.map((template) => {
          const on = template.name === selected
          const scheduled = schedules.some((s) => s.template === template.name)
          return (
            <div
              key={template.name}
              ref={(el) => {
                if (el) options.current.set(template.name, el)
                else options.current.delete(template.name)
              }}
              role="option"
              aria-selected={on}
              aria-label={`Template ${template.name}`}
              aria-describedby={scheduled ? scheduledID : undefined}
              tabIndex={template.name === focusable ? 0 : -1}
              data-indicator-key={template.name}
              onClick={() => onSelect(template.name)}
              onKeyDown={(e) => onOptionKey(e, template.name)}
              className={cn(
                focusRingInset,
                'relative flex h-8 shrink-0 cursor-default items-center gap-2 rounded-control px-2 text-ui outline-none coarse:h-11',
                !on && cn('transition-colors duration-100 motion-reduce:transition-none', raised ? 'hover:bg-hover-chrome' : 'hover:bg-hover'),
              )}
            >
              <AgentGlyph agent={template.harness} />
              <span className={cn('min-w-0 flex-1 truncate', on ? 'font-medium text-text' : 'text-muted')}>{template.name}</span>
              {scheduled && <Clock aria-hidden className="size-3 shrink-0 text-icon-faint" />}
              <Badge className="h-4.5 text-ui-xs">{modeLabel(template.mode)}</Badge>
            </div>
          )
        })}
      </div>
    </div>
  )
}
