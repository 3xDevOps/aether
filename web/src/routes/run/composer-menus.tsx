import { Fragment, useEffect, useRef, useState } from 'react'
import { Check, ChevronDown, FileText, Folder } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command'
import { Menu, MenuContent, MenuLabel, MenuRadioGroup, MenuRadioItem, MenuTrigger } from '@/components/ui/menu'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { api } from '@/lib/api'
import type { ConfigOption, ConfigValue, SessionCommand } from '@/lib/session-types'
import { cn, focusRingInset, surface } from '@/lib/utils'
import { useStore } from '@/store'
import { filesKey } from '@/store/files'

export interface Suggestion {
  id: string
  label: string
  detail?: string
  insert: string
  folder?: boolean
}

export function triggerAt(text: string, caret: number): { kind: '/' | '@'; query: string; start: number } | null {
  const before = text.slice(0, caret)
  const slash = /^\/(\S*)$/.exec(before)
  if (slash) return { kind: '/', query: slash[1]!, start: 0 }
  const at = /(^|\s)@(\S*)$/.exec(before)
  if (at) return { kind: '@', query: at[2]!, start: before.length - at[2]!.length - 1 }
  return null
}

export function commandSuggestions(commands: SessionCommand[], query: string): Suggestion[] {
  return commands
    .filter((command) => command.name.toLowerCase().startsWith(query.toLowerCase()))
    .map((command) => ({ id: command.name, label: `/${command.name}`, detail: command.description, insert: `/${command.name} ` }))
}

export function useFileSuggestions(workspaceID: string, runID: string, query: string | null): Suggestion[] {
  const slash = query === null ? -1 : query.lastIndexOf('/')
  const dir = query === null ? null : query.slice(0, slash + 1)
  const prefix = query === null ? '' : query.slice(slash + 1).toLowerCase()
  const key = dir === null ? null : filesKey(workspaceID, runID, dir.replace(/\/$/, ''))
  const tree = useStore((s) => (key ? s.trees[key] : undefined))
  useEffect(() => {
    if (!key || dir === null || tree) return
    const path = dir.replace(/\/$/, '')
    useStore.getState().setTree(key, { entries: [], loading: true })
    api.filesTree({ workspace_id: workspaceID, run_id: runID, path }).then(
      (result) => useStore.getState().setTree(key, { entries: result.entries }),
      (err: unknown) => useStore.getState().setTree(key, { entries: [], error: String(err) }),
    )
  }, [key, dir, tree, workspaceID, runID])
  if (!tree || dir === null) return []
  return tree.entries
    .filter((entry) => entry.name.toLowerCase().startsWith(prefix))
    .slice(0, 50)
    .map((entry) => ({
      id: `${dir}${entry.name}`,
      label: `${dir}${entry.name}${entry.kind === 'dir' ? '/' : ''}`,
      insert: entry.kind === 'dir' ? `@${dir}${entry.name}/` : `@${dir}${entry.name} `,
      folder: entry.kind === 'dir',
    }))
}

export function SuggestionList({ id, items, active, onPick }: {
  id: string
  items: Suggestion[]
  active: number
  onPick: (item: Suggestion) => void
}) {
  if (items.length === 0) return null
  return (
    <ul id={id} role="listbox" aria-label="Suggestions" className={cn(surface, 'absolute inset-x-0 bottom-full z-10 mb-1 max-h-56 overflow-y-auto p-1')}>
      {items.map((item, index) => (
        <li
          key={item.id}
          id={`${id}-${index}`}
          role="option"
          aria-selected={index === active}
          onMouseDown={(event) => {
            event.preventDefault()
            onPick(item)
          }}
          className={cn(
            focusRingInset,
            'flex h-7 min-w-0 cursor-pointer items-center gap-2 rounded-control px-2 text-ui-sm text-text coarse:h-11',
            index === active && 'bg-hover-chrome',
          )}
        >
          {item.insert.startsWith('@') && (item.folder ? <Folder aria-hidden className="size-3.5 shrink-0 text-muted" /> : <FileText aria-hidden className="size-3.5 shrink-0 text-muted" />)}
          <span className="shrink-0 font-code">{item.label}</span>
          {item.detail && <span className="min-w-0 truncate text-muted">{item.detail}</span>}
        </li>
      ))}
    </ul>
  )
}

const footerCategories = ['mode', 'model', 'thought_level']

type OptionPillProps = {
  option: ConfigOption
  disabled: boolean
  onSet: (option: ConfigOption, value: string) => Promise<void>
}

function OptionPill({ option, disabled, onSet }: OptionPillProps) {
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(false)
  const inFlight = useRef(false)
  useEffect(() => {
    if (disabled) setOpen(false)
  }, [disabled])
  const choices = (option.options ?? []).flatMap((entry) => ('group' in entry ? entry.options : [entry]))
  const current = choices.find((choice) => choice.value === option.currentValue)
  const unavailable = disabled || busy
  const changeOpen = (next: boolean) => {
    if (next && (unavailable || inFlight.current)) return
    setOpen(next)
  }
  const select = (value: string) => {
    if (unavailable || inFlight.current) return
    inFlight.current = true
    setOpen(false)
    setBusy(true)
    void onSet(option, value).finally(() => {
      inFlight.current = false
      setBusy(false)
    })
  }
  // Keep the trigger focusable so Radix can return focus on close, not after the request.
  const trigger = (
    <Button variant="ghost" size="sm" aria-disabled={unavailable || undefined} aria-busy={busy || undefined} aria-label={`${option.name}: ${current?.name ?? 'not set'}`}>
      <span className="max-w-32 truncate">{current?.name ?? option.name}</span>
      <ChevronDown />
    </Button>
  )

  if (option.category === 'model') {
    const item = (choice: ConfigValue, group?: { group: string; name: string }) => (
      <CommandItem
        key={`value:${choice.value}`}
        value={JSON.stringify(choice.value)}
        keywords={[choice.value, choice.name, choice.description ?? '', group?.name ?? '', group?.group ?? '']}
        disabled={unavailable}
        onSelect={() => select(choice.value)}
      >
        <span className="flex size-4 shrink-0 items-center">
          {choice.value === option.currentValue && <Check aria-hidden />}
        </span>
        <span className="flex min-w-0 flex-1 flex-col whitespace-normal py-1 [overflow-wrap:anywhere]">
          <span>{choice.name}</span>
          <span className="text-ui-sm text-muted">{choice.value}</span>
          {choice.description && <span className="text-ui-sm text-muted">{choice.description}</span>}
          {choice.value === option.currentValue && <span className="sr-only">Current model</span>}
        </span>
      </CommandItem>
    )
    return (
      <Popover open={open && !unavailable} onOpenChange={changeOpen}>
        <PopoverTrigger asChild>{trigger}</PopoverTrigger>
        <PopoverContent
          side="top"
          aria-label={option.name}
          className="flex flex-col overflow-hidden"
          onOpenAutoFocus={(event) => {
            const content = event.target as HTMLElement
            const input = content.querySelector<HTMLInputElement>('[cmdk-input]')
            if (input) {
              input.focus({ preventScroll: true })
              event.preventDefault()
            }
            content.querySelector('[cmdk-item][data-selected=true]')?.scrollIntoView({ block: 'nearest' })
          }}
        >
          <Command label={option.name} defaultValue={current ? JSON.stringify(current.value) : undefined} className="[&_[cmdk-input-wrapper]]:shrink-0">
            <CommandInput placeholder={`Search ${option.name.toLowerCase()}…`} aria-label={`Search ${option.name}`} />
            <CommandList className="min-h-0">
              <CommandEmpty>No models found. Try another search.</CommandEmpty>
              {(option.options ?? []).map((entry) => 'group' in entry ? (
                <CommandGroup
                  key={`group:${entry.group}`}
                  heading={entry.name}
                  className="[&_[cmdk-group-heading]]:whitespace-normal [&_[cmdk-group-heading]]:[overflow-wrap:anywhere]"
                >
                  {entry.options.map((choice) => item(choice, entry))}
                </CommandGroup>
              ) : item(entry))}
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    )
  }

  const item = (choice: ConfigValue) => (
    <MenuRadioItem
      key={choice.value}
      value={choice.value}
      description={choice.description ?? null}
      disabled={unavailable}
    >
      {choice.name}
    </MenuRadioItem>
  )
  return (
    <Menu open={open && !unavailable} onOpenChange={changeOpen}>
      <MenuTrigger asChild>{trigger}</MenuTrigger>
      <MenuContent align="start" side="top" className="w-[min(360px,calc(100vw-16px))]">
        <MenuLabel className="whitespace-normal [overflow-wrap:anywhere]">{option.name}</MenuLabel>
        <MenuRadioGroup value={String(option.currentValue ?? '')} onValueChange={select}>
          {(option.options ?? []).map((entry) => 'group' in entry ? (
            <Fragment key={entry.group}>
              <MenuLabel className="whitespace-normal [overflow-wrap:anywhere]">{entry.name}</MenuLabel>
              {entry.options.map(item)}
            </Fragment>
          ) : item(entry))}
        </MenuRadioGroup>
      </MenuContent>
    </Menu>
  )
}

export function OptionPills({ options, disabled, onSet }: {
  options: ConfigOption[]
  disabled: boolean
  onSet: (option: ConfigOption, value: string) => Promise<void>
}) {
  const shown = footerCategories.flatMap((category) => options.filter((option) => option.category === category && option.type === 'select'))
  return (
    <>
      {shown.map((option) => <OptionPill key={option.id} option={option} disabled={disabled} onSet={onSet} />)}
    </>
  )
}
