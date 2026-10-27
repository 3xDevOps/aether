import { useState } from 'react'
import { Ellipsis, Eye, EyeOff, Pencil, Trash2 } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu'
import { RadioPills, RadioPillsItem } from '@/components/ui/radio-pills'
import { RelativeTime } from '@/components/ui/relative-time'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { Preference } from '@/lib/types'
import { cn } from '@/lib/utils'

export const forgetWarning =
  "New conversations and launches won't use it. Runs already started keep the text they were given. Server backups and update snapshots taken earlier may still contain it until replaced. The current assistant conversation keeps it until New conversation. Copies a connected app or agent made aren't removed."

export type Scope = 'all' | 'workspace'

export function ScopePills({ value, onChange, label }: { value: Scope; onChange: (scope: Scope) => void; label: string }) {
  return (
    <RadioPills aria-label={label} value={value} onValueChange={(next) => onChange(next as Scope)}>
      <RadioPillsItem value="all">All my workspaces</RadioPillsItem>
      <RadioPillsItem value="workspace">This workspace</RadioPillsItem>
    </RadioPills>
  )
}

export function sourceLine(preference: Preference): string {
  const day = new Date(preference.created_at).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  if (preference.source === 'assistant') return `You said in the assistant · ${day}`
  return 'Added in Settings'
}

type Mode = 'view' | 'edit' | 'forget'

export function PreferenceRow({ preference, client, workspaceName, activeWorkspace, highlighted, onChanged, onForgotten }: {
  preference: Preference
  client: Api
  workspaceName: string | undefined
  activeWorkspace: string
  highlighted: boolean
  onChanged: (preference: Preference) => void
  onForgotten: (preference: Preference) => void
}) {
  const [mode, setMode] = useState<Mode>('view')
  const [text, setText] = useState(preference.text)
  const [scope, setScope] = useState<Scope>(preference.workspace_id ? 'workspace' : 'all')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const run = async (action: () => Promise<void>) => {
    setBusy(true)
    setError(null)
    try {
      await action()
    } catch (err) {
      setError(errorSentence(err))
    } finally {
      setBusy(false)
    }
  }
  const startEdit = () => {
    setText(preference.text)
    setScope(preference.workspace_id ? 'workspace' : 'all')
    setError(null)
    setMode('edit')
  }
  const save = () => run(async () => {
    const workspaceID = preference.workspace_id ?? activeWorkspace
    const result = await client.preferenceUpdate({
      id: preference.id,
      ...(text !== preference.text ? { text } : {}),
      ...(scope === 'all' && preference.workspace_id ? { clear_workspace: true } : {}),
      ...(scope === 'workspace' && !preference.workspace_id ? { workspace_id: workspaceID } : {}),
    })
    onChanged(result.preference)
    setMode('view')
  })
  const toggle = () => run(async () => onChanged((await client.preferenceUpdate({ id: preference.id, enabled: !preference.enabled })).preference))
  const forget = () => run(async () => {
    await client.preferenceDelete(preference.id)
    onForgotten(preference)
  })
  const scopeLabel = preference.workspace_id ? workspaceName ?? 'One workspace' : 'All my workspaces'
  const changed = text !== preference.text || scope !== (preference.workspace_id ? 'workspace' : 'all')

  return (
    <li
      id={`preference-${preference.id}`}
      data-highlight={highlighted ? '' : undefined}
      className="flex min-w-0 scroll-mt-4 flex-col gap-2 px-3 py-2.5 transition-colors data-highlight:bg-selection motion-reduce:transition-none"
    >
      {mode === 'edit'
        ? (
          <div className="flex min-w-0 flex-col gap-2">
            <Textarea aria-label="Preference text" value={text} rows={2} onChange={(event) => setText(event.target.value)} />
            <ScopePills label="Applies to" value={scope} onChange={setScope} />
            <div className="flex flex-wrap gap-2">
              <Button size="sm" disabled={busy || text.trim() === '' || !changed || (scope === 'workspace' && !preference.workspace_id && !activeWorkspace)} onClick={save}>{busy ? 'Saving…' : 'Save'}</Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => { setError(null); setMode('view') }}>Cancel</Button>
            </div>
          </div>
        )
        : (
          <div className="flex min-w-0 flex-wrap items-start justify-between gap-x-4 gap-y-2">
            <div className="flex min-w-0 flex-[1_1_14rem] flex-col gap-0.5">
              <p className={cn('text-ui font-medium break-words whitespace-pre-wrap text-text', !preference.enabled && 'text-muted line-through')}>{preference.text}</p>
              <p className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-ui-sm text-muted">
                <Badge>{scopeLabel}</Badge>
                {!preference.enabled && <Badge>Off</Badge>}
                <span>{sourceLine(preference)}</span>
              </p>
              <p className="flex flex-wrap gap-x-1.5 text-ui-sm text-muted">
                <span>Updated <RelativeTime at={preference.updated_at} /></span>
                <span>· {preference.last_used_at ? <>Last used <RelativeTime at={preference.last_used_at} /></> : 'Not used yet'}</span>
              </p>
            </div>
            {mode === 'view' && (
              <>
                <div className="flex shrink-0 gap-1 max-sm:hidden">
                  <Button size="sm" variant="ghost" disabled={busy} onClick={startEdit}>Edit</Button>
                  <Button size="sm" variant="ghost" disabled={busy} onClick={toggle}>{preference.enabled ? 'Turn off' : 'Turn on'}</Button>
                  <Button size="sm" variant="ghost" disabled={busy} onClick={() => { setError(null); setMode('forget') }}>Forget</Button>
                </div>
                <div className="sm:hidden">
                  <Menu>
                    <MenuTrigger asChild>
                      <Button size="icon" variant="ghost" label="Actions"><Ellipsis /></Button>
                    </MenuTrigger>
                    <MenuContent align="end">
                      <MenuItem icon={<Pencil />} disabled={busy} onSelect={startEdit}>Edit</MenuItem>
                      <MenuItem icon={preference.enabled ? <EyeOff /> : <Eye />} disabled={busy} onSelect={toggle}>{preference.enabled ? 'Turn off' : 'Turn on'}</MenuItem>
                      <MenuItem tone="danger" icon={<Trash2 />} disabled={busy} onSelect={() => { setError(null); setMode('forget') }}>Forget</MenuItem>
                    </MenuContent>
                  </Menu>
                </div>
              </>
            )}
          </div>
        )}
      {mode === 'forget' && (
        <Callout
          tone="neutral"
          actions={(
            <>
              <Button size="sm" variant="danger" disabled={busy} onClick={forget}>{busy ? 'Forgetting…' : 'Forget'}</Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => setMode('view')}>Cancel</Button>
            </>
          )}
        >
          Forget this preference? {forgetWarning}
        </Callout>
      )}
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
    </li>
  )
}
