import { useEffect, useState } from 'react'
import { ChevronDown, FileText, Folder } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Menu, MenuContent, MenuLabel, MenuRadioGroup, MenuRadioItem, MenuTrigger } from '@/components/ui/menu'
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

function values(option: ConfigOption): ConfigValue[] {
  return (option.options ?? []).flatMap((entry) => ('group' in entry ? entry.options : [entry]))
}

export function OptionPills({ options, disabled, onSet }: {
  options: ConfigOption[]
  disabled: boolean
  onSet: (option: ConfigOption, value: string) => Promise<void>
}) {
  const [busy, setBusy] = useState<string | null>(null)
  const shown = footerCategories
    .map((category) => options.find((option) => option.category === category && option.type === 'select'))
    .filter((option): option is ConfigOption => Boolean(option))
  return (
    <>
      {shown.map((option) => {
        const choices = values(option)
        const current = choices.find((choice) => choice.value === option.currentValue)
        return (
          <Menu key={option.id}>
            <MenuTrigger asChild>
              <Button variant="ghost" size="sm" disabled={disabled || busy === option.id} aria-label={`${option.name}: ${current?.name ?? 'not set'}`}>
                <span className="max-w-32 truncate">{current?.name ?? option.name}</span>
                <ChevronDown />
              </Button>
            </MenuTrigger>
            <MenuContent align="start" side="top">
              <MenuLabel>{option.name}</MenuLabel>
              <MenuRadioGroup
                value={String(option.currentValue ?? '')}
                onValueChange={(value) => {
                  setBusy(option.id)
                  void onSet(option, value).finally(() => setBusy(null))
                }}
              >
                {choices.map((choice) => (
                  <MenuRadioItem key={choice.value} value={choice.value}>
                    <span className="flex min-w-0 flex-col">
                      <span>{choice.name}</span>
                      {choice.description && <span className="text-ui-sm text-muted">{choice.description}</span>}
                    </span>
                  </MenuRadioItem>
                ))}
              </MenuRadioGroup>
            </MenuContent>
          </Menu>
        )
      })}
    </>
  )
}
