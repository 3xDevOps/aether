import { type RefObject, useId, useRef, useState } from 'react'
import { ChevronRight } from '@/components/icons'
import { Input } from '@/components/ui/input'
import { ListRow } from '@/components/ui/list-row'
import { SectionLabel } from '@/components/ui/section-label'
import { type Category, categories, groups, search, type SettingEntry } from '@/routes/settings/registry'
import { useStore } from '@/store'

export function SettingsNav({ input, shown, visible, current, phone }: {
  input: RefObject<HTMLInputElement | null>
  shown: SettingEntry[]
  visible: Category[]
  current?: string
  phone: boolean
}) {
  const navigate = useStore((s) => s.navigate)
  const [query, setQuery] = useState('')
  const results = useRef<HTMLDivElement>(null)
  const id = useId()
  const hits = query.trim() ? search(query, shown) : null
  const titles = new Map(categories.map((category) => [category.id, category.title]))

  const open = (entry: SettingEntry) => {
    setQuery('')
    if (entry.link) navigate(entry.link.name, entry.link.params?.(useStore.getState().activeWorkspace))
    else navigate('settings', { section: entry.id })
  }
  const trailing = phone ? <ChevronRight /> : undefined

  return (
    <nav aria-label="Settings" className="flex min-w-0 flex-col gap-3 p-3">
      <div role="search" className="flex flex-col gap-1">
        <Input
          ref={input}
          type="search"
          aria-label="Search settings"
          placeholder="Search settings"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing) return
            if (e.key === 'Enter' && hits?.[0]) open(hits[0])
            else if (e.key === 'ArrowDown') {
              e.preventDefault()
              results.current?.querySelector('button')?.focus()
            } else if (e.key === 'Escape') {
              e.stopPropagation()
              if (query) setQuery('')
              else input.current?.blur()
            }
          }}
        />
        {hits && (
          <p role="status" className="px-1 text-ui-sm text-muted">
            {hits.length === 0 ? `No settings match “${query.trim()}”` : `${hits.length} ${hits.length === 1 ? 'setting' : 'settings'}`}
          </p>
        )}
      </div>
      {hits ? (
        <div ref={results} aria-label="Search results" role="group" className="flex flex-col">
          {hits.map((entry) => (
            <ListRow key={entry.id} trailing={titles.get(entry.category)} onClick={() => open(entry)}>{entry.label}</ListRow>
          ))}
        </div>
      ) : groups.map((group, index) => {
        const items = visible.filter((category) => category.group === group)
        if (items.length === 0) return null
        return (
          <div key={group} role="group" aria-labelledby={`${id}-${index}`} className="flex flex-col">
            <SectionLabel id={`${id}-${index}`} className="px-2 pb-1">{group}</SectionLabel>
            {items.map(({ id: categoryID, title, Icon }) => (
              <ListRow
                key={categoryID}
                data-settings-category={categoryID}
                selected={categoryID === current}
                aria-current={categoryID === current ? 'page' : undefined}
                leading={<Icon />}
                trailing={trailing}
                onClick={() => navigate('settings', { section: categoryID })}
              >
                {title}
              </ListRow>
            ))}
          </div>
        )
      })}
    </nav>
  )
}
