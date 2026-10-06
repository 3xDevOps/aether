import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { ChevronRight, File, Folder, FolderPlus, GitBranch } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { ListRow } from '@/components/ui/list-row'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { runLabel } from '@/lib/status'
import type { ConfigRoot, Run, Workspace } from '@/lib/types'
import { cn } from '@/lib/utils'
import { type ConfigSource, type FileSource, type SelectFile, type Selection, sourceKey } from '@/routes/files/sources'
import { useStore } from '@/store'
import { configKey } from '@/store/files'

type Expansion = { open: (key: string, fallback: boolean) => boolean; toggle: (key: string, fallback: boolean) => void }

const ExpansionContext = createContext<Expansion>({ open: (_, fallback) => fallback, toggle: () => {} })

/** Keeps folder expansion across the phone sheet remounting the tree. */
export function TreeExpansion({ children }: { children: ReactNode }) {
  const [state, setState] = useState<Record<string, boolean>>({})
  const value: Expansion = {
    open: (key, fallback) => state[key] ?? fallback,
    toggle: (key, fallback) => setState((s) => ({ ...s, [key]: !(s[key] ?? fallback) })),
  }
  return <ExpansionContext.Provider value={value}>{children}</ExpansionContext.Provider>
}

function useExpanded(key: string, fallback: boolean): [boolean, () => void] {
  const { open, toggle } = useContext(ExpansionContext)
  return [open(key, fallback), () => toggle(key, fallback)]
}

const indent = (depth: number) => ({ paddingLeft: 8 + depth * 12 })

export function FileTree({
  workspaces,
  runs,
  configRoots,
  client,
  onSelect,
  selected,
  onNewFile,
}: {
  workspaces: Workspace[]
  runs: Run[]
  configRoots: ConfigRoot[] | null
  client: Api
  onSelect: SelectFile
  selected: Selection | null
  onNewFile: (source: ConfigSource) => void
}) {
  const several = workspaces.length > 1
  return (
    <div className="flex flex-col gap-3 p-2">
      {workspaces.map((workspace) => {
        const checkouts = runs.filter((run) => run.workspace_id === workspace.id)
        const base = { kind: 'workspace' as const, workspaceID: workspace.id, runID: '', label: `base: ${workspace.base_branch}`, branch: workspace.base_branch }
        return (
          <section key={workspace.id} aria-label={workspace.name} className="flex flex-col">
            {several && <p className="truncate px-2 pb-1 text-ui-sm font-medium text-muted">{workspace.name}</p>}
            <TreeDirectory source={base} path="" depth={0} client={client} onSelect={onSelect} selected={selected} icon={<GitBranch />} />
            {checkouts.length > 0 && (
              <Disclosure id={`runs:${workspace.id}`} label="Run checkouts" count={checkouts.length}>
                {checkouts.map((run) => (
                  <TreeDirectory
                    key={run.id}
                    source={{ kind: 'workspace', workspaceID: workspace.id, runID: run.id, label: runLabel(run) }}
                    path=""
                    depth={1}
                    rootOpen={false}
                    client={client}
                    onSelect={onSelect}
                    selected={selected}
                    touched={touchedPaths(run.id)}
                  />
                ))}
              </Disclosure>
            )}
          </section>
        )
      })}
      {configRoots && configRoots.length > 0 && (
        <Disclosure id="config" label="Agent config" count={configRoots.length}>
          {configRoots.map((root) => {
            const source: ConfigSource = { kind: 'config', harness: root.harness, rootPath: root.path, label: root.harness }
            return (
              <TreeDirectory
                key={root.harness}
                source={source}
                path=""
                depth={1}
                rootOpen={false}
                client={client}
                onSelect={onSelect}
                selected={selected}
                trailing={root.path}
                action={
                  <Button variant="ghost" size="icon-sm" label={`New file in ${root.harness}`} onClick={() => onNewFile(source)}>
                    <FolderPlus />
                  </Button>
                }
              />
            )
          })}
        </Disclosure>
      )}
    </div>
  )
}

function Disclosure({ id, label, count, children }: { id: string; label: string; count: number; children: ReactNode }) {
  const [open, toggle] = useExpanded(`disclosure:${id}`, false)
  return (
    <Collapsible open={open} onOpenChange={toggle}>
      <CollapsibleTrigger>
        <span className="min-w-0 flex-1 truncate text-ui-sm font-medium text-muted">{label}</span>
        <span className="pr-1 text-ui-sm text-muted tabular-nums">{count}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>{children}</CollapsibleContent>
    </Collapsible>
  )
}

function touchedPaths(runID: string): Set<string> {
  const snapshots = useStore.getState().diffs[runID]?.snapshots
  return new Set(snapshots?.[0]?.files.map((file) => file.path) ?? [])
}

const noPaths = new Set<string>()

function TreeDirectory({
  source,
  path,
  depth,
  rootOpen = true,
  client,
  onSelect,
  selected,
  touched = noPaths,
  icon,
  trailing,
  action,
}: {
  source: FileSource
  path: string
  depth: number
  rootOpen?: boolean
  client: Api
  onSelect: SelectFile
  selected: Selection | null
  touched?: Set<string>
  icon?: ReactNode
  trailing?: string
  action?: ReactNode
}) {
  const key = sourceKey(source, path)
  const cached = useStore((s) => s.trees[key])
  const filesEpoch = useStore((s) => s.filesEpoch)
  const identityEpoch = useStore((s) => s.identityEpoch)
  const setTree = useStore((s) => s.setTree)
  const [expanded, toggle] = useExpanded(key, path === '' && rootOpen)
  const sourceKind = source.kind
  const harness = source.kind === 'config' ? source.harness : undefined
  const workspaceID = source.kind === 'workspace' ? source.workspaceID : undefined
  const runID = source.kind === 'workspace' ? source.runID : undefined
  const requestID = useRef(0)
  useEffect(() => {
    if (!expanded || cached?.loading || cached?.entries || cached?.error) return
    setTree(key, { entries: [], loading: true, error: undefined })
    const pending = useStore.getState().trees[key]
    const requestToken = ++requestID.current
    const requestIdentityEpoch = identityEpoch
    const stale = () =>
      requestID.current !== requestToken ||
      useStore.getState().identityEpoch !== requestIdentityEpoch ||
      useStore.getState().trees[key] !== pending
    const request = source.kind === 'config'
      ? client.configTree({ harness: source.harness, path })
      : client.filesTree({ workspace_id: source.workspaceID, ...(source.runID ? { run_id: source.runID } : {}), path })
    void request
      .then((result) => {
        if (!stale()) setTree(key, { entries: result.entries, loading: false, error: undefined })
      })
      .catch((err) => {
        if (!stale()) setTree(key, { entries: [], loading: false, error: message(err) })
      })
  }, [cached, client, expanded, filesEpoch, harness, identityEpoch, key, path, runID, setTree, sourceKind, workspaceID])

  const label = path === '' ? source.label : path.split('/').at(-1) ?? path
  return (
    <div>
      <ListRow
        aria-expanded={expanded}
        style={indent(depth)}
        onClick={toggle}
        leading={
          <>
            <ChevronRight className={cn(expanded && 'rotate-90')} />
            {icon ?? <Folder />}
          </>
        }
        trailing={trailing}
        hoverAction={action}
        title={label}
      >
        {label}
      </ListRow>
      {expanded && (
        <div>
          {cached?.loading && <p className="py-1 text-ui-sm text-muted" style={indent(depth + 2)}>Loading files…</p>}
          {cached?.error && <p role="alert" className="py-1 text-ui-sm text-state-failed" style={indent(depth + 2)}>{cached.error}</p>}
          {cached?.entries.map((entry) => {
            const childPath = path ? `${path}/${entry.name}` : entry.name
            if (entry.kind === 'dir') {
              return <TreeDirectory key={childPath} source={source} path={childPath} depth={depth + 1} client={client} onSelect={onSelect} selected={selected} touched={touched} />
            }
            const childKey = sourceKey(source, childPath)
            const isSelected = selected ? sourceKey(selected, selected.path) === childKey : false
            return (
              <ListRow
                key={childPath}
                selected={isSelected}
                style={indent(depth + 1)}
                onClick={() => onSelect(source, childPath)}
                leading={<><span className="w-3.5 shrink-0" /><File /></>}
                trailing={touched.has(childPath) ? <span role="img" aria-label="Changed in this run" title="Changed in this run" className="inline-block size-1.5 rounded-full bg-accent" /> : undefined}
              >
                {entry.name}
              </ListRow>
            )
          })}
        </div>
      )}
    </div>
  )
}

export function NewConfigFile({
  source,
  client,
  onCancel,
  onCreated,
}: {
  source: ConfigSource
  client: Api
  onCancel: () => void
  onCreated: (path: string) => void
}) {
  const [path, setPath] = useState('')
  const [error, setError] = useState<string | null>(null)
  const identityEpoch = useStore((s) => s.identityEpoch)
  return (
    <form
      className="mx-2 mb-2 flex flex-col gap-2 rounded-panel border border-seam bg-canvas p-2"
      onSubmit={(event) => {
        event.preventDefault()
        void createConfigFile(source, path, client, identityEpoch)
          .then((created) => {
            if (created && useStore.getState().identityEpoch === identityEpoch) onCreated(created)
          })
          .catch((err) => {
            if (useStore.getState().identityEpoch === identityEpoch) setError(message(err))
          })
      }}
    >
      <FormField label={`New file in ${source.label}`} help="Relative paths only. Existing files are never overwritten." error={error}>
        <Input
          autoFocus
          value={path}
          onChange={(event) => {
            setPath(event.target.value)
            setError(null)
          }}
          placeholder="settings.json or skills/my-skill.md"
          className="font-code"
        />
      </FormField>
      <div className="flex justify-end gap-2">
        <Button type="button" size="sm" variant="ghost" onClick={onCancel}>Cancel</Button>
        <Button type="submit" size="sm">Create</Button>
      </div>
    </form>
  )
}

async function createConfigFile(source: ConfigSource, raw: string, client: Api, requestEpoch: number): Promise<string | null> {
  const path = raw.trim()
  const pieces = path.split('/')
  if (!path || path.startsWith('/') || path.endsWith('/') || pieces.some((piece) => !piece || piece === '.' || piece === '..')) {
    throw new Error('Use a non-empty relative path without . or .. segments.')
  }
  const result = await client.configWrite({ harness: source.harness, path, content: '', revision: '' })
  const store = useStore.getState()
  if (store.identityEpoch !== requestEpoch) return null
  store.setDocument(configKey(source.harness, path), { ...result, loading: false, error: undefined })
  for (let length = pieces.length - 1; length >= 0; length -= 1) {
    store.invalidateTree(configKey(source.harness, pieces.slice(0, length).join('/')))
  }
  return path
}
