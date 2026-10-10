// The patch `save` sends mirrors protocol.WorkspaceEnvironmentSetParams: only
// what changed, so a secret this page cannot read is kept by not naming it.

import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { X } from '@/components/icons'
import { WorkspaceEnvironmentImport } from '@/components/workspace-environment-import'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Code, CodeBlock } from '@/components/ui/code'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import type { WorkspaceEnvironment, WorkspaceVariable } from '@/lib/types'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'

const scriptHelp = (
  <>
    Runs once in each new run&apos;s container before the agent starts: <Code>sh -e</Code> in <Code>/workspace</Code>,
    as the container&apos;s user, with the variables below. The launch waits for it. If it exits non-zero the launch
    fails and the launch dialog shows what it printed.
  </>
)

const variablesHelp =
  'Set in every new run’s container. Anyone who can launch a run here can print them inside it, secrets included. A secret’s value is never shown on this page again.'

interface Row {
  id: number
  name: string
  value: string
  secret: boolean
  /** The saved variable this row started as; absent for an added one. */
  from?: WorkspaceVariable
  /** A saved secret whose value stays as the server has it. */
  kept: boolean
}

let rowIDs = 0

function savedRow(variable: WorkspaceVariable): Row {
  const secret = variable.secret === true
  return { id: ++rowIDs, name: variable.name, value: variable.value ?? '', secret, from: variable, kept: secret }
}

const untouched = (row: Row) => !row.from && row.name === '' && row.value === ''

interface Problem {
  field: 'name' | 'value'
  text: string
}

function problems(rows: Row[]): Map<number, Problem> {
  const found = new Map<number, Problem>()
  const seen = new Set<string>()
  for (const row of rows) {
    if (untouched(row)) continue
    if (row.name === '') found.set(row.id, { field: 'name', text: 'Give this variable a name.' })
    else if (row.name.includes('=')) found.set(row.id, { field: 'name', text: 'A name cannot contain “=”. Put the value in its own field.' })
    else if (seen.has(row.name)) found.set(row.id, { field: 'name', text: `${row.name} is already in this list.` })
    else if (row.secret && !row.kept && row.value === '') found.set(row.id, { field: 'value', text: 'Enter the secret’s value.' })
    seen.add(row.name)
  }
  return found
}

function changes(environment: WorkspaceEnvironment, script: string, rows: Row[]) {
  const listed = rows.filter((row) => !untouched(row))
  const names = new Set(listed.map((row) => row.name))
  const set = listed
    .filter((row) => {
      if (row.kept) return false
      const from = row.from
      return !(from && from.name === row.name && !from.secret && !row.secret && (from.value ?? '') === row.value)
    })
    .map((row): WorkspaceVariable => (row.secret ? { name: row.name, value: row.value, secret: true } : { name: row.name, value: row.value }))
  return {
    ...(script === environment.setup_script ? {} : { setup_script: script }),
    set,
    unset: environment.variables.map((variable) => variable.name).filter((name) => !names.has(name)),
  }
}

export function WorkspaceEnvironmentSection({ workspaceID, client, editable, reveal }: {
  workspaceID: string
  client: Api
  editable: boolean
  /** Scroll here once loaded: the page was opened for this section. */
  reveal: boolean
}) {
  const [environment, setEnvironment] = useState<WorkspaceEnvironment | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saves, setSaves] = useState(0)
  const pending = environment === null && error === null
  const loading = useDelayed(pending)
  const section = useRef<HTMLDivElement>(null)

  const load = useCallback(() => {
    let live = true
    setError(null)
    client.workspaceEnvironment(workspaceID).then(
      (fresh) => { if (live) setEnvironment(fresh) },
      (err) => { if (live) setError(message(err)) },
    )
    return () => { live = false }
  }, [client, workspaceID])

  useEffect(load, [load])

  useEffect(() => {
    if (reveal && !pending) section.current?.scrollIntoView()
  }, [reveal, pending])

  return (
    <div ref={section} className="scroll-mt-6">
      <SettingsSection title="Environment">
        {environment === null && (
          <div className="px-4 py-3">
            {loading && <Skeleton className="h-24" />}
            {error && (
              <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={load}>Retry</Button>}>
                {error}
              </Callout>
            )}
          </div>
        )}
        {environment !== null && !editable && <ReadOnly environment={environment} />}
        {environment !== null && editable && (
          <Editor
            key={saves}
            environment={environment}
            client={client}
            onSaved={(fresh) => {
              setEnvironment(fresh)
              setSaves((count) => count + 1)
            }}
          />
        )}
      </SettingsSection>
    </div>
  )
}

function ReadOnly({ environment }: { environment: WorkspaceEnvironment }) {
  return (
    <>
      <SettingRow label="Setup script" help={scriptHelp}>
        {environment.setup_script
          ? <CodeBlock aria-label="Setup script">{environment.setup_script}</CodeBlock>
          : <p className="text-ui-sm text-muted">No setup script.</p>}
      </SettingRow>
      <SettingRow label="Variables" help={variablesHelp}>
        {environment.variables.length === 0
          ? <p className="text-ui-sm text-muted">No variables.</p>
          : (
              <ul aria-label="Variables" className="flex min-w-0 flex-col gap-1.5">
                {environment.variables.map((variable) => (
                  <li key={variable.name} className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
                    <Code>{variable.name}</Code>
                    {variable.secret
                      ? <Badge>Secret</Badge>
                      : <span className="min-w-0 font-code text-ui-sm break-all text-muted">{variable.value}</span>}
                  </li>
                ))}
              </ul>
            )}
      </SettingRow>
      <p className="px-4 py-3 text-ui-sm text-muted">Only an admin can change this.</p>
    </>
  )
}

function Editor({ environment, client, onSaved }: { environment: WorkspaceEnvironment; client: Api; onSaved: (fresh: WorkspaceEnvironment) => void }) {
  const [script, setScript] = useState(environment.setup_script)
  const [rows, setRows] = useState(() => environment.variables.map(savedRow))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [checked, setChecked] = useState(false)
  const [importing, setImporting] = useState(false)
  const list = useRef<HTMLUListElement>(null)
  const focusNext = useRef<{ id: number; field: Problem['field'] } | null>(null)

  const change = changes(environment, script, rows)
  const dirty = change.setup_script !== undefined || change.set.length > 0 || change.unset.length > 0
  const wrong = problems(rows)

  const focus = (id: number, field: Problem['field']) =>
    list.current?.querySelector<HTMLInputElement>(`[data-row="${id}"] [data-field="${field}"]`)?.focus()

  // A field that the last change added exists only after its render.
  useEffect(() => {
    if (focusNext.current === null) return
    focus(focusNext.current.id, focusNext.current.field)
    focusNext.current = null
  })

  const edit = (id: number, next: Partial<Row>) => setRows((all) => all.map((row) => (row.id === id ? { ...row, ...next } : row)))

  const add = () => {
    const row: Row = { id: ++rowIDs, name: '', value: '', secret: false, kept: false }
    focusNext.current = { id: row.id, field: 'name' }
    setRows((all) => [...all, row])
  }

  const merge = (incoming: { name: string; value: string }[], secret: boolean) => {
    setRows((all) => {
      const next = [...all]
      for (const { name, value } of incoming) {
        const at = next.findIndex((row) => row.name === name)
        if (at < 0) next.push({ id: ++rowIDs, name, value, secret, kept: false })
        // A saved secret imported as plain is a new plain variable of that name.
        else if (next[at].from?.secret && !secret) next[at] = { id: next[at].id, name, value, secret, kept: false }
        else next[at] = { ...next[at], value, secret, kept: false }
      }
      return next
    })
  }

  const save = async () => {
    setChecked(true)
    const first = rows.find((row) => wrong.has(row.id))
    if (first) {
      focus(first.id, wrong.get(first.id)!.field)
      return
    }
    setBusy(true)
    setError(null)
    try {
      const fresh = await client.workspaceEnvironmentSet({ workspace_id: environment.workspace_id, ...change })
      toast.success('Environment saved')
      onSaved(fresh)
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  const discard = () => {
    setScript(environment.setup_script)
    setRows(environment.variables.map(savedRow))
    setError(null)
    setChecked(false)
  }

  return (
    <>
      <SettingRow label="Setup script" help={scriptHelp} labelFor="workspace-setup-script">
        <Textarea
          id="workspace-setup-script"
          rows={Math.min(16, Math.max(4, script.split('\n').length))}
          className="font-code text-ui-sm"
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          placeholder="npm ci"
          value={script}
          disabled={busy}
          onChange={(event) => setScript(event.target.value)}
        />
      </SettingRow>
      <SettingRow
        label="Variables"
        help={variablesHelp}
        control={(
          <>
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => setImporting(true)}>Import .env…</Button>
            <Button size="sm" variant="secondary" disabled={busy} onClick={add}>Add variable</Button>
          </>
        )}
      >
        {rows.length === 0
          ? <p className="text-ui-sm text-muted">No variables.</p>
          : (
              <ul ref={list} aria-label="Variables" className="flex min-w-0 flex-col gap-5 sm:gap-2">
                {rows.map((row) => (
                  <VariableRow
                    key={row.id}
                    row={row}
                    problem={checked ? wrong.get(row.id) : undefined}
                    disabled={busy}
                    onEdit={(next) => edit(row.id, next)}
                    onReplace={() => {
                      focusNext.current = { id: row.id, field: 'value' }
                      edit(row.id, { kept: false, value: '' })
                    }}
                    onRemove={() => setRows((all) => all.filter((other) => other.id !== row.id))}
                  />
                ))}
              </ul>
            )}
      </SettingRow>
      {error && (
        <div className="px-4 py-3">
          <Callout tone="failed" role="alert" title="Not saved">{error}</Callout>
        </div>
      )}
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-x-6 gap-y-2 px-4 py-3">
        <p className="min-w-0 flex-[1_1_16rem] text-ui-sm text-muted">
          {dirty && <span className="font-medium text-text">Unsaved changes. </span>}
          Runs launched after you save get this. Containers that already exist keep what they started with.
        </p>
        <div className="flex shrink-0 items-center gap-2">
          {dirty && <Button variant="secondary" disabled={busy} onClick={discard}>Discard</Button>}
          <Button disabled={!dirty || busy} onClick={() => void save()}>{busy ? 'Saving…' : 'Save'}</Button>
        </div>
      </div>
      {importing && (
        <WorkspaceEnvironmentImport
          current={rows.filter((row) => !untouched(row)).map((row) => row.name)}
          onAdd={merge}
          onClose={() => setImporting(false)}
        />
      )}
    </>
  )
}

function VariableRow({ row, problem, disabled, onEdit, onReplace, onRemove }: {
  row: Row
  problem?: Problem
  disabled: boolean
  onEdit: (next: Partial<Row>) => void
  onReplace: () => void
  onRemove: () => void
}) {
  const named = row.name || 'this variable'
  const savedSecret = row.from?.secret === true
  const problemID = `workspace-variable-${row.id}-problem`
  return (
    <li data-row={row.id} className="flex min-w-0 flex-col gap-1">
      <div className="grid min-w-0 grid-cols-[minmax(0,1fr)_5rem] items-center gap-2 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)_5rem_auto]">
        <Input
          aria-label="Name"
          data-field="name"
          aria-invalid={problem?.field === 'name' ? true : undefined}
          aria-describedby={problem?.field === 'name' ? problemID : undefined}
          className="font-code text-ui-sm max-sm:col-start-1 max-sm:row-start-1"
          placeholder="NAME"
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          value={row.name}
          readOnly={savedSecret}
          disabled={disabled}
          onChange={(event) => onEdit({ name: event.target.value })}
        />
        {row.kept
          ? (
              <div className="flex min-w-0 items-center gap-2">
                <Input aria-label={`Value of ${named}, hidden`} className="font-code text-ui-sm" value="••••••••" readOnly disabled={disabled} />
                <Button size="sm" variant="secondary" disabled={disabled} onClick={onReplace}>Replace</Button>
              </div>
            )
          : (
              <div className="flex min-w-0 items-center gap-2">
                <Input
                  aria-label={`Value of ${named}`}
                  data-field="value"
                  aria-invalid={problem?.field === 'value' ? true : undefined}
                  aria-describedby={problem?.field === 'value' ? problemID : undefined}
                  className="font-code text-ui-sm"
                  placeholder={savedSecret ? 'New value' : 'value'}
                  type={row.secret ? 'password' : 'text'}
                  autoComplete="off"
                  spellCheck={false}
                  autoCapitalize="off"
                  autoCorrect="off"
                  value={row.value}
                  disabled={disabled}
                  onChange={(event) => onEdit({ value: event.target.value })}
                />
                {savedSecret && (
                  <Button size="sm" variant="secondary" disabled={disabled} onClick={() => onEdit({ kept: true, value: '' })}>Keep current</Button>
                )}
              </div>
            )}
        {savedSecret
          ? <span><Badge>Secret</Badge></span>
          : (
              <label className="flex items-center gap-1.5 text-ui text-text">
                <Checkbox checked={row.secret} disabled={disabled} onCheckedChange={(next) => onEdit({ secret: next === true })} />
                Secret
              </label>
            )}
        <Button variant="ghost" size="icon-sm" label={`Remove ${named}`} className="max-sm:col-start-2 max-sm:row-start-1 max-sm:justify-self-end" disabled={disabled} onClick={onRemove}>
          <X />
        </Button>
      </div>
      {problem && <p id={problemID} className="text-ui-sm text-state-failed">{problem.text}</p>}
    </li>
  )
}
