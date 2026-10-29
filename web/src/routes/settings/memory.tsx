import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { useAssistantAvailable } from '@/components/assistant/button'
import { Brain } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { EmptyState } from '@/components/ui/empty-state'
import { FailureCallout } from '@/components/ui/failure-callout'
import { Input } from '@/components/ui/input'
import { InsetList } from '@/components/ui/inset-list'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { prefersReducedMotion, useDelayed } from '@/lib/hooks'
import { type Restore, restorePreference } from '@/lib/preferences'
import type { Preference } from '@/lib/types'
import { forgetWarning, PreferenceRow, type Scope, ScopePills } from '@/routes/settings/memory-row'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'

export const preferencesLabel = 'Use my preferences in the assistant'

const highlightMs = 1500

function newest(list: Preference[]): Preference[] {
  return [...list].sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))
}

/** Settings › Memory: the member's private assistant preferences. Keyed by identity, so another member never inherits the list. */
export function MemorySection({ client }: { client: Api }) {
  const identity = useStore((s) => s.identityKey)
  return <MemoryBody key={identity} client={client} />
}

function MemoryBody({ client }: { client: Api }) {
  const assistant = useAssistantAvailable()
  const entry = useStore((s) => (s.route.name === 'settings' ? s.route.params.entry : undefined))
  const workspaces = useStore((s) => s.workspaces)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const [list, setList] = useState<Preference[]>([])
  const [enabled, setEnabled] = useState<boolean | null>(null)
  const [load, setLoad] = useState<{ kind: 'loading' } | { kind: 'ready' } | { kind: 'failed'; error: string }>({ kind: 'loading' })
  const [attempt, setAttempt] = useState(0)
  const [query, setQuery] = useState('')
  const [text, setText] = useState('')
  const [scope, setScope] = useState<Scope>('all')
  const [adding, setAdding] = useState(false)
  const [addError, setAddError] = useState<string | null>(null)
  const [toggleError, setToggleError] = useState<string | null>(null)
  const [forgotten, setForgotten] = useState<{ preference: Preference; error?: string } | null>(null)
  const [undoing, setUndoing] = useState(false)
  const reloaded = useRef<string | null>(null)
  const restore = useRef<Restore>({})
  const [saving, setSaving] = useState(false)
  const navigate = useStore((s) => s.navigate)
  const [flash, setFlash] = useState<string | null>(null)
  const skeleton = useDelayed(load.kind === 'loading')
  const textID = useId()
  const searchID = useId()

  useEffect(() => {
    let live = true
    setLoad({ kind: 'loading' })
    client.preferenceList({ include_disabled: true }).then(
      (result) => {
        if (!live) return
        setList(result.preferences)
        setEnabled(result.enabled)
        setLoad({ kind: 'ready' })
      },
      (err: unknown) => live && setLoad({ kind: 'failed', error: errorSentence(err) }),
    )
    return () => {
      live = false
    }
  }, [client, attempt])

  const found = entry !== undefined && list.some((p) => p.id === entry)
  // The assistant may have saved it since the list was read.
  useEffect(() => {
    if (!entry || found || load.kind !== 'ready' || reloaded.current === entry) return
    reloaded.current = entry
    setAttempt((n) => n + 1)
  }, [entry, found, load.kind])
  useEffect(() => {
    if (!entry || !found) return
    document.getElementById(`preference-${entry}`)?.scrollIntoView?.({ block: 'center', behavior: prefersReducedMotion() ? 'auto' : 'smooth' })
    setQuery('')
    setFlash(entry)
    const timer = setTimeout(() => {
      setFlash(null)
      navigate('settings', { section: 'memory' })
    }, highlightMs)
    return () => clearTimeout(timer)
  }, [entry, found, navigate])

  const shown = useMemo(() => {
    const words = query.toLowerCase().split(/\s+/).filter(Boolean)
    return newest(list).filter((p) => words.every((word) => p.text.toLowerCase().includes(word)))
  }, [list, query])

  const put = (preference: Preference) => setList((current) => current.some((p) => p.id === preference.id) ? current.map((p) => p.id === preference.id ? preference : p) : [...current, preference])

  const add = async () => {
    setAdding(true)
    setAddError(null)
    const sent = text.trim()
    try {
      const result = await client.preferenceAdd({ text: sent, ...(scope === 'workspace' && activeWorkspace ? { workspace_id: activeWorkspace } : {}) })
      put(result.preference)
      setText((current) => (current.trim() === sent ? '' : current))
    } catch (err) {
      setAddError(errorSentence(err))
    } finally {
      setAdding(false)
    }
  }

  const setUse = async (next: boolean) => {
    const identity = useStore.getState().identityKey
    setToggleError(null)
    setSaving(true)
    try {
      const settings = await client.assistantSettings({ preferences_enabled: next })
      if (useStore.getState().identityKey !== identity) return
      setEnabled(settings.preferences_enabled !== false)
      const status = useStore.getState().assistantStatus
      if (status) useStore.getState().setAssistantStatus({ ...status, ...settings })
    } catch (err) {
      setToggleError(errorSentence(err))
    } finally {
      setSaving(false)
    }
  }

  const undoForget = async () => {
    if (!forgotten || undoing) return
    const gone = forgotten.preference
    const settle = (next: { preference: Preference; error?: string } | null) => setForgotten((current) => (current?.preference === gone ? next : current))
    settle({ preference: gone })
    setUndoing(true)
    try {
      const back = await restorePreference(client, gone, restore.current)
      if (back) put(back)
      restore.current = {}
      settle(null)
    } catch (err) {
      settle({ preference: gone, error: errorSentence(err) })
    } finally {
      setUndoing(false)
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-col gap-1 text-ui text-muted">
        <p>Only you can see these. Teammates, admins, your runs and connected apps can&apos;t. Server operators can read the server database.</p>
        <p>The assistant uses them when they fit; what you say in a message always wins.</p>
      </div>
      {load.kind === 'failed' && <FailureCallout error={load.error} onRetry={() => setAttempt((n) => n + 1)} />}
      {entry && load.kind === 'ready' && !found && <Callout tone="neutral">That preference no longer exists. It may have been forgotten.</Callout>}
      {assistant && (
        <SettingsSection title="Assistant" id="preferences-enabled" scope="Your account">
          <SettingRow
            label={preferencesLabel}
            labelFor="settings-preferences-enabled"
            help={enabled === false
              ? "Off. The assistant doesn't read or save your preferences. They stay here until you turn this on."
              : 'On. The assistant reads these when they fit and saves new ones when you ask it to remember.'}
            control={(
              <Checkbox
                id="settings-preferences-enabled"
                checked={enabled !== false}
                disabled={enabled === null || saving}
                onCheckedChange={(checked) => void setUse(checked === true)}
              />
            )}
          >
            {toggleError && <Callout tone="failed" role="alert">{toggleError}</Callout>}
          </SettingRow>
        </SettingsSection>
      )}
      <section id="preference-list" aria-label="Preferences" className="flex min-w-0 scroll-mt-4 flex-col gap-3">
        <form
          className="flex min-w-0 flex-col gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            if (text.trim() && !adding) void add()
          }}
        >
          <label htmlFor={textID} className="text-ui font-medium text-text">Add a preference</label>
          <Textarea id={textID} rows={2} value={text} placeholder="Prefer table output" onChange={(event) => setText(event.target.value)} />
          <ScopePills label="Applies to" value={scope === 'workspace' && !activeWorkspace ? 'all' : scope} onChange={setScope} />
          {addError && <Callout tone="failed" role="alert">{addError}</Callout>}
          <Button type="submit" size="sm" className="self-start" disabled={adding || text.trim() === ''}>{adding ? 'Adding…' : 'Add'}</Button>
        </form>
        {forgotten && (
          <Callout
            tone="neutral"
            role="status"
            title={`Forgotten: ${forgotten.preference.text}`}
            actions={(
              <>
                <Button size="sm" variant="secondary" disabled={undoing} onClick={() => void undoForget()}>Undo</Button>
                <Button size="sm" variant="ghost" onClick={() => setForgotten(null)}>Dismiss</Button>
              </>
            )}
          >
            Forgotten. {forgetWarning}
            {forgotten.error && <span role="alert" className="mt-1 block text-state-failed">{forgotten.error}</span>}
          </Callout>
        )}
        {list.length > 0 && (
          <div className="flex flex-col gap-1">
            <label htmlFor={searchID} className="sr-only">Search preferences</label>
            <Input id={searchID} type="search" placeholder="Search preferences" value={query} onChange={(event) => setQuery(event.target.value)} />
          </div>
        )}
        {skeleton && load.kind === 'loading' && (
          <div aria-hidden className="flex flex-col gap-1.5">
            {[0, 1, 2].map((i) => <Skeleton key={i} className="h-14" />)}
          </div>
        )}
        {shown.length > 0 && (
          <InsetList aria-label="Preferences">
            {shown.map((preference) => (
              <PreferenceRow
                key={preference.id}
                preference={preference}
                client={client}
                workspaceName={preference.workspace_id ? workspaces[preference.workspace_id]?.name : undefined}
                activeWorkspace={activeWorkspace}
                highlighted={flash === preference.id}
                onChanged={put}
                onForgotten={(gone) => {
                  setList((current) => current.filter((p) => p.id !== gone.id))
                  restore.current = {}
                  setForgotten({ preference: gone })
                  if (gone.id === entry) navigate('settings', { section: 'memory' })
                }}
              />
            ))}
          </InsetList>
        )}
        {load.kind === 'ready' && list.length > 0 && shown.length === 0 && <p className="text-ui text-muted">No preference matches “{query}”.</p>}
        {load.kind === 'ready' && list.length === 0 && (
          <EmptyState icon={<Brain />} title="Nothing remembered yet" className="py-10">
            Tell the assistant &ldquo;remember that I prefer table output&rdquo;, or add a preference above.
          </EmptyState>
        )}
      </section>
    </div>
  )
}
