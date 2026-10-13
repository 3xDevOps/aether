import { useCallback, useEffect, useState } from 'react'
import type * as React from 'react'
import { Check, RefreshCw } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Command, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command'
import { Popover, PopoverContent, PopoverFieldTrigger } from '@/components/ui/popover'
import { Spinner } from '@/components/ui/spinner'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'

/** The models `harness` offers on the account, fetched the first time a field shows them. */
export function useAgentModels(harness: string, accountMemberID?: string) {
  const key = `${accountMemberID ?? ''}/${harness}`
  const entry = useStore((s) => s.agentModels[key])
  const load = useCallback((refresh: boolean) => {
    const { agentModels, setAgentModels } = useStore.getState()
    setAgentModels(key, { result: agentModels[key]?.result, loading: true })
    api.agentModels(harness, accountMemberID, refresh).then(
      (result) => useStore.getState().setAgentModels(key, { result }),
      (err: unknown) => useStore.getState().setAgentModels(key, { result: useStore.getState().agentModels[key]?.result, error: message(err) }),
    )
  }, [key, harness, accountMemberID])
  useEffect(() => {
    if (harness && !useStore.getState().agentModels[key]) load(false)
  }, [key, harness, load])
  return { ...entry, refresh: () => load(true) }
}

const sourceNotes: Record<string, string> = { cache: "From the agent's cache", fallback: 'Fallback list' }

/** A model for `harness`: "" is the agent's own default, and any typed id is accepted. */
export function ModelPicker({ harness, accountMemberID, value, onChange, disabled = false, className, ...trigger }: {
  harness: string
  accountMemberID?: string
  value: string
  onChange: (model: string) => void
  disabled?: boolean
} & Omit<React.ComponentProps<'button'>, 'value' | 'onChange'>) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const { result, loading, error, refresh } = useAgentModels(harness, accountMemberID)
  const models = result?.models ?? []
  const selected = models.find((model) => model.id === value)
  const typed = search.trim()
  const failure = error ?? result?.error
  const note = result ? sourceNotes[result.source] : undefined
  const pick = (model: string) => {
    onChange(model)
    setOpen(false)
    setSearch('')
  }
  const item = (id: string, name: string, details: (string | undefined)[], keywords: string[]) => (
    <CommandItem key={`model:${id}`} value={JSON.stringify(id)} keywords={keywords} onSelect={() => pick(id)}>
      <span className="flex size-4 shrink-0 items-center">{id === value && <Check aria-hidden />}</span>
      <span className="flex min-w-0 flex-1 flex-col whitespace-normal py-1 [overflow-wrap:anywhere]">
        <span>{name}</span>
        {details.filter(Boolean).map((detail, index) => <span key={index} className="text-ui-sm text-muted">{detail}</span>)}
        {id === value && <span className="sr-only">Current model</span>}
      </span>
    </CommandItem>
  )

  return (
    <Popover modal open={open && !disabled} onOpenChange={(next) => { setOpen(next); if (!next) setSearch('') }}>
      <PopoverFieldTrigger disabled={disabled} className={className} {...trigger}>
        <span className={cn('min-w-0 flex-1 truncate', !value && 'text-muted')}>{value ? (selected?.name ?? value) : 'Agent default'}</span>
      </PopoverFieldTrigger>
      <PopoverContent
        aria-label="Models"
        className="flex w-[min(max(var(--radix-popover-trigger-width),320px),calc(100vw-16px))] flex-col overflow-hidden"
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
        <Command label="Models" defaultValue={JSON.stringify(value)} className="[&_[cmdk-input-wrapper]]:shrink-0">
          <CommandInput value={search} onValueChange={setSearch} placeholder="Search or enter a model id…" aria-label="Search models" />
          <div className="flex min-h-7 shrink-0 items-center gap-2 px-2 text-ui-sm text-muted coarse:min-h-11">
            {loading ? (
              <>
                <Spinner />
                <span>Loading models…</span>
              </>
            ) : failure ? (
              <span className="min-w-0 py-1 break-words text-state-failed">{failure}</span>
            ) : note && <span>{note}</span>}
            <Button type="button" variant="ghost" size="icon-sm" className="ml-auto" label="Refresh models" disabled={loading} onClick={refresh}>
              <RefreshCw aria-hidden />
            </Button>
          </div>
          <CommandList className="min-h-0">
            <CommandGroup value="models">
              {item('', 'Agent default', [result?.default_label], ['Agent default', result?.default_label ?? ''])}
              {value && !selected && item(value, value, [], [value])}
              {models.map((model) => item(model.id, model.name, [model.id === model.name ? undefined : model.id, model.description], [model.id, model.name, model.description ?? '']))}
            </CommandGroup>
            {typed && typed !== value && !models.some((model) => model.id === typed) && (
              <CommandGroup forceMount value="custom">
                <CommandItem forceMount value={`use:${typed}`} onSelect={() => pick(typed)}>
                  <span className="size-4 shrink-0" />
                  <span className="min-w-0 flex-1 py-1 [overflow-wrap:anywhere]">Use “{typed}”</span>
                </CommandItem>
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
