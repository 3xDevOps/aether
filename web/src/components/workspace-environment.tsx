// The patch `save` sends mirrors protocol.WorkspaceEnvironmentSetParams: only
// what changed, so a secret this page cannot read is kept by not naming it.

import { type ChangeEvent, useCallback, useEffect, useRef, useState } from 'react'
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
import { formatBytes, message } from '@/lib/format'
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

const filesHelp =
  'Written into every new run’s checkout at their path before the setup script runs, mode 0600, and kept out of its commits. Anyone who can launch a run here can read them inside it. A file’s content is never shown on this page again.'

const egressHelp = (
  <>
    Runs launched while this is on reach the internet only through Aether&apos;s proxy, and only the hosts listed
    here, which start as the ones agents and package managers need. Turning it on or off applies to runs launched
    afterwards; the host list applies at once to runs started with it on. <Code>*.example.com</Code> allows every
    subdomain of example.com.
  </>
)

const hostPattern = /^(\*\.)?([a-z0-9_-]+\.)*[a-z0-9_-]+$/

interface Row {
  id: number
  name: string
  value: string
  secret: boolean
  /** The saved variable this row started as; absent for an added one. */
  from?: WorkspaceVariable
  /** A saved secret whose value stays as the server has it. */
  kept: boolean
  /** The value arrived with a line break, which a one-line field would drop. */
  lines: boolean
}

let rowIDs = 0

function savedRow(variable: WorkspaceVariable): Row {
  const secret = variable.secret === true
  const value = variable.value ?? ''
  return { id: ++rowIDs, name: variable.name, value, secret, from: variable, kept: secret, lines: value.includes('\n') }
}

const untouched = (row: Row) => !row.from && row.name === '' && row.value === ''

/** protocol.MaxWorkspaceVariableValueBytes, checked before a larger file is read. */
const maxFileBytes = 64 << 10

interface FileEntry {
  id: number
  path: string
  size: number
  /** Only a file added on this page has it: the server never returns one. */
  content?: string
}

const savedFiles = (environment: WorkspaceEnvironment): FileEntry[] =>
  (environment.files ?? []).map((file) => ({ id: ++rowIDs, ...file }))

const shownFiles = (files: FileEntry[]) =>
  files.filter((file) => file.content !== undefined || !files.some((other) => other.content !== undefined && other.path === file.path))

function fileProblems(files: FileEntry[]): Map<number, string> {
  const found = new Map<number, string>()
  const seen = new Set<string>()
  for (const file of files) {
    if (file.content === undefined) continue
    if (file.path === '') found.set(file.id, 'Give this file a path.')
    else if (seen.has(file.path)) found.set(file.id, `${file.path} is already in this list.`)
    seen.add(file.path)
  }
  return found
}

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

function changes(environment: WorkspaceEnvironment, script: string, rows: Row[], files: FileEntry[], egress: boolean, hosts: string[]) {
  const listed = rows.filter((row) => !untouched(row))
  const names = new Set(listed.map((row) => row.name))
  const set = listed
    .filter((row) => {
      if (row.kept) return false
      const from = row.from
      return !(from && from.name === row.name && !from.secret && !row.secret && (from.value ?? '') === row.value)
    })
    .map((row): WorkspaceVariable => (row.secret ? { name: row.name, value: row.value, secret: true } : { name: row.name, value: row.value }))
  const added = files.flatMap(({ path, content }) => (content === undefined ? [] : [{ path, content }]))
  const kept = new Set(files.flatMap(({ path, content }) => (content === undefined ? [path] : [])))
  const removed = (environment.files ?? []).map((file) => file.path).filter((path) => !kept.has(path))
  const saved = environment.egress_hosts ?? []
  const allow = hosts.filter((host) => !saved.includes(host))
  const deny = saved.filter((host) => !hosts.includes(host))
  return {
    ...(script === environment.setup_script ? {} : { setup_script: script }),
    set,
    unset: environment.variables.map((variable) => variable.name).filter((name) => !names.has(name)),
    ...(added.length > 0 ? { files: added } : {}),
    ...(removed.length > 0 ? { remove_files: removed } : {}),
    ...(egress === (environment.egress ?? false) ? {} : { egress }),
    ...(allow.length > 0 ? { egress_allow: allow } : {}),
    ...(deny.length > 0 ? { egress_deny: deny } : {}),
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
      <SettingsSection title="Environment" heading="h2">
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
                      : <span className="min-w-0 font-code text-ui-sm break-all whitespace-pre-wrap text-muted">{variable.value}</span>}
                  </li>
                ))}
              </ul>
            )}
      </SettingRow>
      {environment.files && (
        <SettingRow label="Files" help={filesHelp}>
          {environment.files.length === 0
            ? <p className="text-ui-sm text-muted">No files.</p>
            : (
                <ul aria-label="Files" className="flex min-w-0 flex-col gap-1.5">
                  {environment.files.map((file) => (
                    <li key={file.path} className="flex min-w-0 items-baseline gap-2">
                      <Code className="min-w-0">{file.path}</Code>
                      <span className="shrink-0 text-ui-sm text-muted tabular-nums">{formatBytes(file.size)}</span>
                    </li>
                  ))}
                </ul>
              )}
        </SettingRow>
      )}
      {environment.egress_hosts && (
        <SettingRow label="Limit outbound traffic" help={egressHelp}>
          <p className="text-ui-sm text-muted">{environment.egress ? 'On.' : 'Off.'}</p>
          {environment.egress && environment.egress_hosts.length > 0 && <HostList hosts={environment.egress_hosts} />}
        </SettingRow>
      )}
      <p className="px-4 py-3 text-ui-sm text-muted">Only an admin can change this.</p>
    </>
  )
}

function HostList({ hosts, disabled, onRemove }: { hosts: string[]; disabled?: boolean; onRemove?: (host: string) => void }) {
  return (
    <ul aria-label="Allowed hosts" className="flex min-w-0 flex-wrap gap-1.5">
      {hosts.map((host) => (
        <li key={host} className="flex min-w-0 items-center gap-0.5">
          <Code>{host}</Code>
          {onRemove && (
            <Button variant="ghost" size="icon-sm" label={`Remove ${host}`} disabled={disabled} onClick={() => onRemove(host)}>
              <X />
            </Button>
          )}
        </li>
      ))}
    </ul>
  )
}

function Editor({ environment, client, onSaved }: { environment: WorkspaceEnvironment; client: Api; onSaved: (fresh: WorkspaceEnvironment) => void }) {
  const [script, setScript] = useState(environment.setup_script)
  const [rows, setRows] = useState(() => environment.variables.map(savedRow))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [checked, setChecked] = useState(false)
  const [importing, setImporting] = useState(false)
  const [egress, setEgress] = useState(environment.egress ?? false)
  const [hosts, setHosts] = useState(environment.egress_hosts ?? [])
  const [draft, setDraft] = useState('')
  const [draftProblem, setDraftProblem] = useState<string | null>(null)
  const [files, setFiles] = useState(() => savedFiles(environment))
  const [fileError, setFileError] = useState<string | null>(null)
  const list = useRef<HTMLUListElement>(null)
  const fileList = useRef<HTMLUListElement>(null)
  const picker = useRef<HTMLInputElement>(null)
  const focusNext = useRef<{ id: number; field: Problem['field'] } | null>(null)

  const change = changes(environment, script, rows, files, egress, hosts)
  const dirty = change.setup_script !== undefined || change.set.length > 0 || change.unset.length > 0 ||
    change.files !== undefined || change.remove_files !== undefined ||
    change.egress !== undefined || change.egress_allow !== undefined || change.egress_deny !== undefined
  const wrong = problems(rows)
  const wrongFiles = fileProblems(files)

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
    const row: Row = { id: ++rowIDs, name: '', value: '', secret: false, kept: false, lines: false }
    focusNext.current = { id: row.id, field: 'name' }
    setRows((all) => [...all, row])
  }

  const merge = (incoming: { name: string; value: string }[], asSecrets: boolean) => {
    setRows((all) => {
      const next = [...all]
      for (const { name, value } of incoming) {
        const at = next.findIndex((row) => row.name === name)
        // An empty value has nothing to hide and is added plain. Over a saved
        // secret it stays a secret, so that Save asks for a value instead of
        // removing the one the server holds.
        const secret = asSecrets && (value !== '' || next[at]?.from?.secret === true)
        const imported = { name, value, secret, kept: false, lines: value.includes('\n') }
        if (at < 0) next.push({ id: ++rowIDs, ...imported })
        // A saved secret imported as plain is a new plain variable of that name.
        else if (next[at].from?.secret && !secret) next[at] = { id: next[at].id, ...imported }
        else next[at] = { ...next[at], ...imported }
      }
      return next
    })
  }

  const addFile = async (file: File | undefined) => {
    if (!file) return
    setFileError(null)
    if (file.size > maxFileBytes) {
      setFileError(`${file.name} is ${formatBytes(file.size)}. A workspace file holds at most 64 KB.`)
      return
    }
    let content: string
    try {
      // ignoreBOM keeps a byte order mark in the content instead of dropping it.
      content = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(await file.arrayBuffer())
    } catch (err) {
      setFileError(err instanceof TypeError ? `${file.name} is not UTF-8 text. A workspace file must be.` : `${file.name}: ${message(err)}`)
      return
    }
    setFiles((all) => [...all, { id: ++rowIDs, path: file.name, size: file.size, content }])
  }

  const save = async () => {
    setChecked(true)
    const first = rows.find((row) => wrong.has(row.id))
    if (first) {
      focus(first.id, wrong.get(first.id)!.field)
      return
    }
    const firstFile = files.find((file) => wrongFiles.has(file.id))
    if (firstFile) {
      fileList.current?.querySelector<HTMLInputElement>(`[data-file="${firstFile.id}"] input`)?.focus()
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
    setFiles(savedFiles(environment))
    setFileError(null)
    setEgress(environment.egress ?? false)
    setHosts(environment.egress_hosts ?? [])
    setDraft('')
    setDraftProblem(null)
    setError(null)
    setChecked(false)
  }

  const allow = () => {
    const host = draft.trim().toLowerCase().replace(/\.$/, '')
    if (!hostPattern.test(host)) setDraftProblem('Enter a host such as example.com or *.example.com, without https:// or a port.')
    else if (hosts.includes(host)) setDraftProblem(`${host} is already allowed.`)
    else {
      setHosts([...hosts, host].sort())
      setDraft('')
      setDraftProblem(null)
    }
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
                    onPasteLines={(value) => {
                      focusNext.current = { id: row.id, field: 'value' }
                      edit(row.id, { value, lines: true })
                    }}
                    onReplace={() => {
                      focusNext.current = { id: row.id, field: 'value' }
                      edit(row.id, { kept: false, value: '', lines: false })
                    }}
                    onRemove={() => setRows((all) => all.filter((other) => other.id !== row.id))}
                  />
                ))}
              </ul>
            )}
      </SettingRow>
      {environment.files && (
        <SettingRow
          label="Files"
          help={filesHelp}
          control={<Button size="sm" variant="secondary" disabled={busy} onClick={() => picker.current?.click()}>Add file…</Button>}
        >
          <div
            className="flex min-w-0 flex-col gap-2"
            onDragOver={(event) => event.preventDefault()}
            onDrop={(event) => {
              event.preventDefault()
              if (!busy) void addFile(event.dataTransfer.files[0])
            }}
          >
            {files.length === 0
              ? <p className="text-ui-sm text-muted">No files. Choose Add file… or drop one here.</p>
              : (
                  <ul ref={fileList} aria-label="Files" className="flex min-w-0 flex-col gap-2">
                    {shownFiles(files).map((file) => {
                      const problem = checked ? wrongFiles.get(file.id) : undefined
                      const problemID = `workspace-file-${file.id}-problem`
                      return (
                        <li key={file.id} data-file={file.id} className="flex min-w-0 flex-col gap-1">
                          <div className="flex min-w-0 items-center gap-2">
                            {file.content === undefined
                              ? <Code className="min-w-0 flex-1">{file.path}</Code>
                              : (
                                  <Input
                                    aria-label="Path"
                                    aria-invalid={problem ? true : undefined}
                                    aria-describedby={problem ? problemID : undefined}
                                    className="font-code text-ui-sm"
                                    placeholder=".env"
                                    spellCheck={false}
                                    autoCapitalize="off"
                                    autoCorrect="off"
                                    value={file.path}
                                    disabled={busy}
                                    onChange={(event) => {
                                      const path = event.target.value
                                      setFiles((all) => all.map((other) => (other.id === file.id ? { ...other, path } : other)))
                                    }}
                                  />
                                )}
                            <span className="shrink-0 text-ui-sm text-muted tabular-nums">{formatBytes(file.size)}</span>
                            <Button variant="ghost" size="icon-sm" label={`Remove ${file.path || 'this file'}`} disabled={busy} onClick={() => setFiles((all) => all.filter((other) => other.id !== file.id))}>
                              <X />
                            </Button>
                          </div>
                          {problem && <p id={problemID} className="text-ui-sm text-state-failed">{problem}</p>}
                        </li>
                      )
                    })}
                  </ul>
                )}
            {fileError && <Callout tone="failed" role="alert">{fileError}</Callout>}
            <input
              ref={picker}
              type="file"
              className="sr-only"
              tabIndex={-1}
              aria-label="Choose a file"
              onChange={(event) => {
                const file = event.currentTarget.files?.[0]
                event.currentTarget.value = ''
                void addFile(file)
              }}
            />
          </div>
        </SettingRow>
      )}
      {environment.egress_hosts && (
        <SettingRow
          label="Limit outbound traffic"
          labelFor="workspace-egress"
          help={egressHelp}
          control={<Checkbox id="workspace-egress" checked={egress} disabled={busy} onCheckedChange={(next) => setEgress(next === true)} />}
        >
          {egress && (
            <>
              {hosts.length === 0
                ? <p className="text-ui-sm text-muted">No host is allowed.</p>
                : <HostList hosts={hosts} disabled={busy} onRemove={(host) => setHosts(hosts.filter((other) => other !== host))} />}
              <div className="flex min-w-0 flex-col gap-1">
                <div className="flex min-w-0 items-center gap-2">
                  <Input
                    aria-label="Host to allow"
                    aria-invalid={draftProblem ? true : undefined}
                    aria-describedby={draftProblem ? 'workspace-egress-problem' : undefined}
                    className="font-code text-ui-sm"
                    placeholder="example.com"
                    spellCheck={false}
                    autoCapitalize="off"
                    autoCorrect="off"
                    value={draft}
                    disabled={busy}
                    onChange={(event) => setDraft(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key !== 'Enter') return
                      event.preventDefault()
                      allow()
                    }}
                  />
                  <Button size="sm" variant="secondary" disabled={busy || draft.trim() === ''} onClick={allow}>Allow</Button>
                </div>
                {draftProblem && <p id="workspace-egress-problem" className="text-ui-sm text-state-failed">{draftProblem}</p>}
              </div>
            </>
          )}
        </SettingRow>
      )}
      {error && (
        <div className="px-4 py-3">
          <Callout tone="failed" role="alert" title="Not saved">{error}</Callout>
        </div>
      )}
      {environment.warning && !error && (
        <div className="px-4 py-3">
          <Callout tone="needs-you" role="status" title="Saved">{environment.warning}</Callout>
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

function VariableRow({ row, problem, disabled, onEdit, onPasteLines, onReplace, onRemove }: {
  row: Row
  problem?: Problem
  disabled: boolean
  onEdit: (next: Partial<Row>) => void
  /** The value after a paste that holds a line break, which the one-line field would drop. */
  onPasteLines: (value: string) => void
  onReplace: () => void
  onRemove: () => void
}) {
  const named = row.name || 'this variable'
  const savedSecret = row.from?.secret === true
  const problemID = `workspace-variable-${row.id}-problem`
  const field = {
    'aria-label': `Value of ${named}`,
    'data-field': 'value',
    'aria-invalid': problem?.field === 'value' ? true : undefined,
    'aria-describedby': problem?.field === 'value' ? problemID : undefined,
    className: 'font-code text-ui-sm',
    spellCheck: false,
    autoCapitalize: 'off',
    autoCorrect: 'off',
    value: row.value,
    disabled,
    onChange: (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => onEdit({ value: event.target.value }),
  }
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
        {row.kept || (row.secret && row.lines)
          ? (
              <div className="flex min-w-0 items-center gap-2">
                <Input aria-label={`Value of ${named}, hidden`} className="font-code text-ui-sm" value="••••••••" readOnly disabled={disabled} />
                <Button data-field="value" size="sm" variant="secondary" disabled={disabled} onClick={onReplace}>Replace</Button>
              </div>
            )
          : (
              <div className="flex min-w-0 items-center gap-2">
                {row.lines
                  ? <Textarea {...field} wrap="off" rows={Math.min(8, row.value.split('\n').length)} />
                  : (
                      <Input
                        {...field}
                        placeholder={savedSecret ? 'New value' : 'value'}
                        type={row.secret ? 'password' : 'text'}
                        autoComplete="off"
                        onPaste={(event) => {
                          const pasted = event.clipboardData.getData('text').replace(/\r\n/g, '\n')
                          if (!pasted.trimEnd().includes('\n')) return
                          event.preventDefault()
                          const { selectionStart, selectionEnd } = event.currentTarget
                          onPasteLines(row.value.slice(0, selectionStart ?? row.value.length) + pasted + row.value.slice(selectionEnd ?? row.value.length))
                        }}
                      />
                    )}
                {savedSecret && (
                  <Button size="sm" variant="secondary" disabled={disabled} onClick={() => onEdit({ kept: true, value: '', lines: false })}>Keep current</Button>
                )}
              </div>
            )}
        {savedSecret
          ? <span><Badge>Secret</Badge></span>
          : (
              <label className="flex items-center gap-1.5 text-ui text-text">
                <Checkbox checked={row.secret} disabled={disabled} onCheckedChange={(next) => onEdit({ secret: next === true, lines: row.value.includes('\n') })} />
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
