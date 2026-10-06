// The browser only reads the chosen directory; the server validates and
// secret-scans the import.

import { useEffect, useRef, useState } from 'react'
import type { ChangeEvent } from 'react'
import { Button } from '@/components/ui/button'
import { ApiError } from '@/lib/api'
import type { Api } from '@/lib/api'
import { formatBytes, message } from '@/lib/format'
import type {
  ConfigExclusion,
  ConfigFile,
  ConfigRoot,
} from '@/lib/types'
import { useStore } from '@/store'
import type { ConfigImportCandidate, ConfigImportStatus } from '@/store/ui'

export const IMPORT_BATCH_FILES = 2000
export const IMPORT_BATCH_BYTES = 20 * 1024 * 1024
export const MAX_IMPORT_FILE_BYTES = 64 * 1024 * 1024

interface PathParts {
  root: string
  path: string
  valid: boolean
}

interface LocalExclusion extends ConfigExclusion {
  detail: string
}

interface Selection {
  basename: string
  files: ConfigImportCandidate[]
  bytes: number
  excluded: LocalExclusion[]
}

function relativePath(file: File): PathParts {
  const candidate =
    (file as File & { webkitRelativePath?: string }).webkitRelativePath ||
    file.name
  const normalized = candidate.replaceAll('\\', '/')
  const parts = normalized.split('/')
  if (parts.length < 2 || parts.some((part) => part === '' || part === '.' || part === '..')) {
    return { root: '', path: '', valid: false }
  }
  const root = parts.shift() ?? ''
  const path = parts.join('/')
  if (!root || !path || path.includes('\0')) {
    return { root: '', path: '', valid: false }
  }
  return { root, path, valid: true }
}

function rootName(path: string): string {
  const normalized = path.replaceAll('\\', '/').replace(/\/+$/, '')
  const slash = normalized.lastIndexOf('/')
  return slash < 0 ? normalized : normalized.slice(slash + 1)
}

function isCredential(path: string, credentialNames: Set<string>): boolean {
  const parts = path.split('/').map((part) => part.toLowerCase())
  return parts.some((part) => credentialNames.has(part) || part.endsWith('.pem'))
}

function isRuntime(path: string, runtimeIgnores: string[]): boolean {
  return runtimeIgnores.some((entry) => {
    const ignored = entry.replace(/\/+$/, '')
    return ignored !== '' && (path === ignored || path.startsWith(`${ignored}/`))
  })
}

function readBytes(file: File): Promise<ArrayBuffer> {
  if (typeof file.arrayBuffer === 'function') return file.arrayBuffer()
  const { promise, resolve, reject } = Promise.withResolvers<ArrayBuffer>()
  const reader = new FileReader()
  reader.onload = () => {
    if (reader.result instanceof ArrayBuffer) resolve(reader.result)
    else reject(new Error('The selected file did not return bytes'))
  }
  reader.onerror = () =>
    reject(reader.error ?? new Error('Could not read the selected file'))
  reader.readAsArrayBuffer(file)
  return promise
}

function base64(bytes: ArrayBuffer): string {
  const values = new Uint8Array(bytes)
  let binary = ''
  for (let offset = 0; offset < values.length; offset += 0x8000) {
    binary += String.fromCharCode(...values.subarray(offset, offset + 0x8000))
  }
  return btoa(binary)
}

function exclusion(path: string, reason: string, detail: string): LocalExclusion {
  return { path, reason, detail }
}

/** Prepare paths and metadata without reading file contents. */
export async function prepareDirectoryImport(
  selected: File[] | FileList,
  root: ConfigRoot,
  generationIsCurrent: () => boolean = () => true,
): Promise<Selection | null> {
  const files = Array.from(selected)
  if (files.length === 0) return null
  if (!Array.isArray(root.credential_names)) {
    throw new Error('The server did not provide credential exclusions. Update the server and reload before importing.')
  }
  const credentialNames = new Set(root.credential_names.map((name) => name.toLowerCase()))
  const first = relativePath(files[0])
  if (!first.valid) throw new Error('Choose a directory, not an individual file.')
  const basename = first.root
  const excluded: LocalExclusion[] = []
  const payload: Selection['files'] = []
  const paths = new Set<string>()
  const rootPrefix = root.path.substring(2)
  let bytes = 0

  for (const file of files) {
    if (!generationIsCurrent()) return null
    const parts = relativePath(file)
    const displayPath = parts.valid ? parts.path : file.name
    if (!parts.valid || parts.root !== basename) {
      throw new Error(`${displayPath}: path is not a file below the selected directory. Nothing was uploaded.`)
    }
    // Match configPath's single prefix removal, but retain the original request
    // path so the server does not strip a second prefix from nested directories.
    if (parts.path === rootPrefix) {
      throw new Error(`${parts.path}: path names the destination root, not a file. Nothing was uploaded.`)
    }
    const destinationPath = parts.path.startsWith(`${rootPrefix}/`)
      ? parts.path.slice(rootPrefix.length + 1)
      : parts.path
    if (isCredential(parts.path, credentialNames)) {
      excluded.push(
        exclusion(parts.path, 'credential', 'credential file excluded before upload'),
      )
      continue
    }
    if (destinationPath === '.aether-profile-ignore') {
      excluded.push(exclusion(parts.path, 'policy', 'CLI ignore rules do not apply to browser imports'))
      continue
    }
    if (isRuntime(parts.path, root.runtime_ignores) || isRuntime(destinationPath, root.runtime_ignores)) {
      excluded.push(
        exclusion(parts.path, 'runtime', 'runtime history excluded before upload'),
      )
      continue
    }
    if (paths.has(destinationPath)) {
      throw new Error(`${parts.path}: duplicate destination ${destinationPath} selected. Nothing was uploaded.`)
    }
    paths.add(destinationPath)
    const problem = file.size > MAX_IMPORT_FILE_BYTES
      ? exclusion(parts.path, 'size', `${file.size} bytes exceeds the 64 MiB (${MAX_IMPORT_FILE_BYTES} bytes) browser import limit`)
      : undefined
    payload.push({ path: parts.path, destinationPath, file, problem })
    bytes += file.size
  }

  return { basename, files: payload, bytes, excluded }
}

// The operation belongs to the authenticated owner, not the mounted route.
async function uploadDirectory(
  client: Api,
  owner: string | null,
  root: ConfigRoot,
  selection: Selection,
  previous?: ConfigImportStatus,
) {
  const harness = root.harness
  let status: ConfigImportStatus = {
    owner,
    basename: selection.basename,
    destination: root.path,
    totalFiles: previous?.totalFiles ?? selection.files.length + selection.excluded.length,
    excluded: (previous?.excluded ?? []).concat(selection.excluded),
    phase: 'reading',
    result: previous
      ? { ...previous.result, error: undefined }
      : { harness, files: 0, bytes: 0, excluded: [], imported_paths: [] },
    unknownPaths: previous?.unknownPaths ?? [],
    remaining: [],
    errors: previous?.errors ?? [],
  }
  let identityChanged = false
  const unsubscribe = useStore.subscribe((state) => {
    if (state.identityKey !== owner) identityChanged = true
  })
  const checkIdentity = () => {
    if (identityChanged || useStore.getState().identityKey !== owner) {
      throw new Error('Authenticated member or server changed. No further batches were sent.')
    }
  }
  useStore.setState({ configImportPending: true, configImportStatus: status })
  let inFlightPaths: string[] = []
  let readFailure: ConfigExclusion | undefined
  try {
    for (let offset = 0; offset < selection.files.length;) {
      checkIdentity()
      status = { ...status, phase: 'reading' }
      useStore.setState({ configImportStatus: status })
      const files: ConfigFile[] = []
      const destinationPaths: string[] = []
      let bytes = 0
      while (offset < selection.files.length && files.length < IMPORT_BATCH_FILES) {
        const { path, destinationPath, file } = selection.files[offset]
        if (files.length > 0 && bytes + file.size > IMPORT_BATCH_BYTES) break
        try {
          const content = await readBytes(file)
          checkIdentity()
          if (content.byteLength > MAX_IMPORT_FILE_BYTES) {
            throw new Error('file exceeds the 64 MiB browser import limit')
          }
          if (content.byteLength !== file.size) {
            throw new Error('file size changed after selection; choose the directory again')
          }
          files.push({ path, content_base64: base64(content), mode: 0o644 })
          bytes += content.byteLength
        } catch (err) {
          readFailure = exclusion(path, 'read', message(err))
          throw new Error(`${path}: ${message(err)}`)
        }
        destinationPaths.push(destinationPath)
        offset += 1
        if (bytes >= IMPORT_BATCH_BYTES) break
      }
      checkIdentity()
      status = { ...status, phase: 'uploading' }
      useStore.setState({ configImportStatus: status })
      checkIdentity()
      inFlightPaths = destinationPaths
      const imported = await client.configImport({ harness, files })
      inFlightPaths = []
      const excludedPaths = new Set(imported.excluded.map(({ path }) => path))
      const importedPaths = imported.error
        ? (imported.imported_paths ?? [])
        : destinationPaths.filter((path) => !excludedPaths.has(path))
      status = {
        ...status,
        result: {
          harness,
          files: status.result.files + imported.files,
          bytes: status.result.bytes + imported.bytes,
          excluded: status.result.excluded.concat(imported.excluded),
          imported_paths: (status.result.imported_paths ?? []).concat(importedPaths),
          ...(imported.error ? { error: imported.error } : {}),
        },
      }
      if (imported.error) {
        status.errors = status.errors.concat(imported.error)
        break
      }
      checkIdentity()
    }
  } catch (err) {
    // Only an explicit pre-dispatch refusal proves a submitted batch did not run.
    if (err instanceof ApiError && err.data && typeof err.data === 'object' &&
      'config_import_not_started' in err.data && err.data.config_import_not_started === true) {
      inFlightPaths = []
    }
    const detail = message(err)
    status = {
      ...status,
      result: {
        ...status.result,
        error: inFlightPaths.length > 0
          ? `Import outcome is unknown for the current batch: ${detail}. Some files in this batch may have been copied.`
          : detail,
      },
      unknownPaths: status.unknownPaths.concat(inFlightPaths),
      errors: status.errors.concat(detail),
    }
  } finally {
    unsubscribe()
    const settled = new Set([
      ...(status.result.imported_paths ?? []),
      ...status.result.excluded.map(({ path }) => path),
      ...status.unknownPaths,
    ])
    const remaining = selection.files
      .filter(({ destinationPath }) => !settled.has(destinationPath))
      .map((entry) => readFailure?.path === entry.path ? { ...entry, problem: readFailure } : entry)
    useStore.setState({
      configImportPending: false,
      configImportStatus: {
        ...status,
        phase: 'complete',
        remaining: identityChanged ? [] : remaining,
      },
    })
  }
}

function ExclusionList({ entries, label }: { entries: ConfigExclusion[]; label: string }) {
  if (entries.length === 0) return null
  return (
    <div className="space-y-2 border-t border-border/70 pt-2">
      <p className="text-sm font-medium">{label}: {entries.length}</p>
      <ul className="max-h-52 min-w-0 space-y-1 overflow-y-auto text-xs">
        {entries.map((entry, index) => (
          <li key={`${entry.path}-${index}`}>
            <span className="font-mono">{entry.path}</span>
            <span className="text-muted-foreground">
              {' '}- {entry.detail ? `${entry.reason}: ${entry.detail}` : entry.reason}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function ProfileImport({ client }: { client: Api }) {
  const identityKey = useStore((state) => state.identityKey)
  useEffect(() => {
    const status = useStore.getState().configImportStatus
    if (status && status.owner !== identityKey && status.remaining.length > 0) {
      useStore.setState({ configImportStatus: { ...status, remaining: [] } })
    }
  }, [identityKey])
  // File handles reset with identity; an in-flight operation outlives the form.
  return <ProfileImportForm key={identityKey} client={client} identityKey={identityKey} />
}

function ProfileImportForm({ client, identityKey }: { client: Api; identityKey: string | null }) {
  const navigate = useStore((state) => state.navigate)
  const [roots, setRoots] = useState<ConfigRoot[] | null>(null)
  const [rootsError, setRootsError] = useState<string | null>(null)
  const [rawFiles, setRawFiles] = useState<File[] | null>(null)
  const [selection, setSelection] = useState<Selection | null>(null)
  const [selectedHarness, setSelectedHarness] = useState('')
  const [reading, setReading] = useState(false)
  const [selectionError, setSelectionError] = useState<string | null>(null)
  const [omitted, setOmitted] = useState<Set<string>>(new Set())
  const [recovering, setRecovering] = useState(false)
  const status = useStore((state) =>
    state.configImportStatus?.owner === identityKey ? state.configImportStatus : null,
  )
  const result = status?.phase === 'complete' ? status.result : null
  const importing = useStore((state) => state.configImportPending)
  const picker = useRef<HTMLInputElement | null>(null)
  const generation = useRef(0)

  const importStarted = useRef(false)
  useEffect(() => {
    let active = true
    setRoots(null)
    setRootsError(null)
    client
      .configRoots()
      .then((response) => {
        if (active) setRoots(response.roots)
      })
      .catch((err) => {
        if (active) setRootsError(message(err))
      })
    return () => {
      active = false
      generation.current += 1
    }
  }, [client])

  const firstPath = rawFiles ? relativePath(rawFiles[0]) : null
  const basename = firstPath?.valid ? firstPath.root : ''
  const matchingRoots = basename && roots
    ? roots.filter((root) => rootName(root.path) === basename)
    : []
  const destinationRoots = roots ?? []
  const agentLabel = (harness: string) =>
    destinationRoots.find((root) => root.harness === harness)?.display_name ?? harness
  const destination = destinationRoots.find(
    (root) => root.harness === selectedHarness,
  )
  const included = selection?.files.filter(({ path }) => !omitted.has(path)) ?? []
  const blocked = included.filter(({ problem }) => problem)
  const selectedBytes = included.reduce((total, { file }) => total + file.size, 0)
  const exclusions = (selection?.excluded ?? []).concat(
    selection?.files.filter(({ path }) => omitted.has(path)).map(({ path, problem }) =>
      exclusion(path, problem?.reason ?? 'user', problem?.detail
        ? `explicitly excluded: ${problem.detail}`
        : 'excluded by you before upload'),
    ) ?? [],
  )

  // A directory can be selected while roots are loading. Pick the destination
  // from metadata once it arrives, but do not read bytes until that happens.
  useEffect(() => {
    if (!rawFiles || roots === null || !basename) return
    setSelectedHarness((current) => {
      if (current && roots.some((root) => root.harness === current)) return current
      const matching = roots.filter((root) => rootName(root.path) === basename)
      return matching.length === 1 ? matching[0].harness : ''
    })
  }, [basename, rawFiles, roots])

  useEffect(() => {
    if (!rawFiles || roots === null || !destination || importing || (status && !recovering) || importStarted.current) return
    const version = ++generation.current
    let active = true
    setSelection(null)
    setReading(true)
    setOmitted(new Set())
    setSelectionError(null)
    void prepareDirectoryImport(
      rawFiles,
      destination,
      () => active && generation.current === version,
    )
      .then((prepared) => {
        if (!active || generation.current !== version) return
        if (prepared && recovering && status) {
          const problems = new Map(status.remaining.map((entry) => [entry.path, entry.problem]))
          prepared.files = prepared.files.map((entry) => ({
            ...entry, problem: entry.problem ?? problems.get(entry.path),
          }))
        }
        setSelection(prepared)
        if (!prepared) setSelectionError('The selected directory could not be read.')
      })
      .catch((err) => {
        if (active && generation.current === version) setSelectionError(message(err))
      })
      .finally(() => {
        if (active && generation.current === version) setReading(false)
      })
    return () => {
      active = false
    }
  }, [destination, importing, rawFiles, status, roots, recovering])

  function chooseDestination(harness: string) {
    if (useStore.getState().configImportPending || result) return
    importStarted.current = false
    generation.current += 1
    setSelectedHarness(harness)
    setSelection(null)
    setReading(false)
    setSelectionError(null)
  }

  function choose(event: ChangeEvent<HTMLInputElement>) {
    if (useStore.getState().configImportPending) return
    importStarted.current = false
    const selected = Array.from(event.currentTarget.files ?? [])
    event.currentTarget.value = ''
    if (selected.length === 0) return
    setRecovering(false)
    generation.current += 1
    setRawFiles(null)
    setSelection(null)
    setSelectedHarness('')
    setReading(false)
    setSelectionError(null)
    useStore.setState({ configImportStatus: null })
    setOmitted(new Set())
    const first = relativePath(selected[0])
    if (!first.valid) {
      setSelectionError('Choose a directory, not an individual file.')
      return
    }
    setRawFiles(selected)
    if (roots !== null) {
      const matching = roots.filter((root) => rootName(root.path) === first.root)
      if (matching.length === 1) setSelectedHarness(matching[0].harness)
    }
  }
  function reviewRemaining() {
    if (!status || importing || status.remaining.length === 0) return
    const root = roots?.find((entry) => entry.harness === status.result.harness)
    if (!root || root.path !== status.destination) {
      setSelectionError('The original destination is no longer available. Choose the directory again and review a new destination.')
      return
    }
    importStarted.current = false
    generation.current += 1
    setSelection(null)
    setRawFiles(status.remaining.map(({ file }) => file))
    setSelectedHarness(root.harness)
    setSelectionError(null)
    setRecovering(true)
  }

  function toggleFile(path: string, include: boolean) {
    setOmitted((current) => {
      const next = new Set(current)
      if (include) next.delete(path)
      else next.add(path)
      return next
    })
  }


  async function importConfiguration() {
    if (
      !selection ||
      blocked.length > 0 ||
      !destination ||
      reading ||
      (result && !recovering) ||
      useStore.getState().identityKey !== identityKey ||
      useStore.getState().configImportPending
    ) return
    importStarted.current = true
    const previous = recovering && status ? status : undefined
    setRecovering(false)
    await uploadDirectory(client, identityKey, destination, {
      ...selection, files: included, bytes: selectedBytes, excluded: exclusions,
    }, previous)
  }
  const rootLabel = basename || 'your agent configuration directory'

  return (
    <section
      aria-label="Agent config files"
      className="min-w-0 space-y-4 border-t border-border/70 py-3"
    >
      <div className="space-y-1">
        <p className="text-sm leading-6 text-muted-foreground">
          Choose an agent configuration directory to import. You can return and
          import another directory or updated files whenever needed. Supported
          roots include <span className="font-mono">~/.claude</span>,{' '}
          <span className="font-mono">~/.codex</span>,{' '}
          <span className="font-mono">~/.pi</span>, and{' '}
          <span className="font-mono">~/.omp</span>.
        </p>
      </div>
      {importing && !status && (
        <p role="status" className="text-sm text-muted-foreground">Waiting for the previous import to settle…</p>
      )}

      {roots === null && !rootsError && !importing && (
        <p role="status" className="text-sm text-muted-foreground">
          Loading configuration destinations…
        </p>
      )}
      {rootsError && (
        <div className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2">
          <p className="text-sm text-state-failed">Loading configuration destinations failed: {rootsError}</p>
        </div>
      )}
      {roots !== null && roots.length === 0 && (
        <p className="border-y border-border/70 bg-card px-3 py-2.5 text-sm text-muted-foreground">
          This account has no supported configuration directories.
        </p>
      )}

      <div className="border-y border-border/70 bg-card px-3 py-2.5">
        <div className="flex min-w-0 flex-wrap items-center gap-3 text-sm font-medium">
          <label htmlFor="configuration-directory-picker">Configuration directory</label>
          <input
            id="configuration-directory-picker"
            {...({ webkitdirectory: '' } as Record<string, string>)}
            ref={picker}
            type="file"
            multiple
            disabled={importing}
            aria-label="Choose configuration directory"
            className="sr-only"
            onChange={choose}
          />
          <Button
            type="button"
            size="sm"
            variant="secondary"
            disabled={importing}
            onClick={() => picker.current?.click()}
          >
            Choose directory
          </Button>
        </div>
        {basename && (
          <p className="mt-2 text-sm">
            Selected directory: <span className="break-all font-mono">{basename}</span>
          </p>
        )}
        <p className="mt-2 text-[13px] leading-5 text-muted-foreground">
          Known credential files and the selected agent's runtime files are
          left out before reading. Other files are sent to the server for
          checking when you confirm. Only checked files are transferred, in bounded
          batches. There is no directory count or total-size limit. Each file may be up to 64 MiB.
        </p>
      </div>

      {rawFiles && roots !== null && roots.length > 0 && (
        <label className="flex min-w-0 flex-wrap items-center gap-3 text-sm">
          <span className="font-medium">
            {matchingRoots.length === 1 ? 'Destination' : 'Choose destination'}
          </span>
          <select
              aria-label="Configuration destination"
              className="h-7 min-w-44 max-w-full rounded-sm border border-input bg-background px-2 text-sm coarse:h-11 coarse:text-base"
              value={selectedHarness}
              disabled={importing || Boolean(result)}
              onChange={(event) => chooseDestination(event.target.value)}
            >
              <option value="">Select an agent</option>
              {destinationRoots.map((root) => (
                <option key={root.harness} value={root.harness}>
                  {root.display_name ?? root.harness} ({root.path})
                </option>
              ))}
          </select>
        </label>
      )}

      {reading && (
        <p className="border-l-2 border-state-working/60 bg-state-working/5 px-3 py-2 text-sm" role="status">
          Preparing {rootLabel}; file contents will be read only when you import.
        </p>
      )}
      {selectionError && (
        <p className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-sm text-state-failed" role="alert">
          {selectionError}
        </p>
      )}
      {selection && !reading && (!status || recovering) && (
        <div className="min-w-0 space-y-3 border-y border-border/70 bg-card px-3 py-3">
          <div className="space-y-1">
            <h4 className="text-sm font-semibold">Preview</h4>
            <p className="text-sm">
              {included.length} files, {formatBytes(selectedBytes)} selected from{' '}
              <span className="font-mono">{selection.basename}</span>.
            </p>
            <p className="text-[13px] leading-5 text-muted-foreground">
              Empty and binary files are preserved. {exclusions.length} files left out
              before upload. Uncheck anything you do not want copied.
            </p>
          </div>

          <div className="space-y-2 border-t border-border/70 pt-2">
            <p className="text-sm font-medium">Select files</p>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="secondary" onClick={() => setOmitted(new Set())}>Select all</Button>
              <Button size="sm" variant="secondary" onClick={() => setOmitted(new Set(selection.files.map(({ path }) => path)))}>Exclude all</Button>
              {selection.files.some(({ problem }) => problem) && (
                <Button size="sm" variant="secondary" onClick={() => setOmitted((current) =>
                  new Set([...current, ...selection.files.filter(({ problem }) => problem).map(({ path }) => path)]),
                )}>
                  Exclude unsupported files
                </Button>
              )}
            </div>
            <ul className="max-h-64 min-w-0 space-y-2 overflow-y-auto text-xs">
              {selection.files.map(({ path, file, problem }) => (
                <li key={path} className="space-y-1">
                  <label className="flex items-start gap-2">
                    <input type="checkbox" aria-label={`Include ${path}`} checked={!omitted.has(path)}
                      onChange={(event) => toggleFile(path, event.target.checked)} />
                    <span className="min-w-0 break-all font-mono">{path}</span>
                    <span className="shrink-0 text-muted-foreground">{formatBytes(file.size)}</span>
                  </label>
                  {problem && (
                    <p className="break-words text-state-failed">
                      {problem.detail}
                      {problem.reason === 'read' && (
                        <Button size="sm" variant="secondary" onClick={() => setSelection({
                          ...selection,
                          files: selection.files.map((entry) => entry.path === path ? { ...entry, problem: undefined } : entry),
                        })}>Retry reading on import</Button>
                      )}
                    </p>
                  )}
                </li>
              ))}
            </ul>
          </div>
          {blocked.length > 0 && (
            <p role="alert" className="text-sm text-state-failed">
              {blocked.length} selected files cannot be imported. Explicitly exclude them to continue with the rest.
              Nothing in this review has been uploaded.
            </p>
          )}
          <ExclusionList entries={exclusions} label="Left out before upload" />
          <p className="text-[13px] leading-5 text-muted-foreground">
            For a file over 64 MiB, exclude it here, then use <span className="font-mono">aether terminal</span> to
            install or download that asset directly into the destination in your persistent home.
            Files is a text editor, not an alternative large-file uploader.
          </p>

          <p className="border-l-2 border-state-working/60 bg-state-working/5 px-3 py-2 text-[13px] leading-5">
            Before you confirm: matching remote configuration files will be
            overwritten, accepted files change your persistent remote home
            immediately, and this local directory will not be watched.
          </p>
          <Button
            size="sm"
            disabled={importing || reading || blocked.length > 0 || !destination}
            onClick={() => void importConfiguration()}
          >
            Import configuration
          </Button>
        </div>
      )}
      {status && (
        <div className="min-w-0 space-y-3 border-y border-border/70 bg-card px-3 py-3">
          <p className="text-sm">
            Directory: <span className="break-all font-mono">{status.basename}</span>
            {' '}Destination: <span className="font-mono">{status.destination}</span>
          </p>
          {!result && (
            <p role="status" className="border-l-2 border-state-working/60 bg-state-working/5 px-3 py-2 text-sm">
              Importing configuration: {status.phase === 'reading' ? 'reading' : 'uploading'} the next batch.
              {' '}{status.result.files} files ({formatBytes(status.result.bytes)}) confirmed imported;
              {' '}{status.result.excluded.length} server exclusions from {status.totalFiles} selected files.
              You can navigate away and return to this result. Keep this browser window open.
            </p>
          )}
          {result && (
            <>
              {result.error || status.unknownPaths.length > 0 ? (
                <>
                  <div className="space-y-2 border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-sm text-state-failed" role="alert">
                    <p>
                      Import incomplete: {result.files} files ({formatBytes(result.bytes)}) imported into{' '}
                      {agentLabel(result.harness)}.
                    </p>
                    <p>{result.error}</p>
                    <p>Copied files remain. Review failed or unattempted files below to continue.</p>
                    {status.unknownPaths.length > 0 && (
                      <p>Inspect Files before explicitly retrying any unknown outcome by choosing the directory again.</p>
                    )}
                    <Button size="sm" variant="secondary" onClick={() => navigate('files')}>
                      Inspect Files
                    </Button>
                  </div>
                  <div className="space-y-2 border-t border-border/70 pt-2">
                    <p className="text-sm font-medium">Imported paths: {result.imported_paths?.length ?? 0}</p>
                    <ul className="max-h-52 min-w-0 space-y-1 overflow-y-auto text-xs">
                      {result.imported_paths?.map((path) => (
                        <li key={path} className="break-all font-mono">{path}</li>
                      ))}
                    </ul>
                  </div>
                  {status.unknownPaths.length > 0 && (
                    <details className="space-y-2 border-t border-border/70 pt-2">
                      <summary className="cursor-pointer text-sm font-medium">
                        Paths with unknown outcome: {status.unknownPaths.length}
                      </summary>
                      <ul className="max-h-52 min-w-0 space-y-1 overflow-y-auto text-xs">
                        {status.unknownPaths.map((path) => (
                          <li key={path} className="break-all font-mono">{path}</li>
                        ))}
                      </ul>
                    </details>
                  )}
                </>
              ) : (
                <div className="border-l-2 border-border bg-muted/20 px-3 py-2 text-sm" role="status">
                  <p>Imported {result.files} files ({formatBytes(result.bytes)}) into{' '}
                    {agentLabel(result.harness)}.</p>
                  {status.excluded.length + result.excluded.length > 0 && (
                    <p>Import finished with omissions: {status.excluded.length + result.excluded.length} files were not imported. Review every exclusion below.</p>
                  )}
                </div>
              )}
              {status.errors.length > (result.error ? 1 : 0) && (
                <details>
                  <summary className="cursor-pointer text-sm">Earlier import errors</summary>
                  <ul className="space-y-1 text-xs">
                    {status.errors.map((error, index) => <li key={index}>{error}</li>)}
                  </ul>
                </details>
              )}
              {status.remaining.length > 0 && (
                <div className="space-y-2 border-t border-border/70 pt-2">
                  <p className="text-sm font-medium">Failed or unattempted paths: {status.remaining.length}</p>
                  <ul className="max-h-52 space-y-1 overflow-y-auto text-xs">
                    {status.remaining.map(({ destinationPath, problem }) => (
                      <li key={destinationPath}><span className="font-mono">{destinationPath}</span>{problem?.detail && ` - ${problem.detail}`}</li>
                    ))}
                  </ul>
                  {!recovering && <Button size="sm" variant="secondary" disabled={roots === null} onClick={reviewRemaining}>Review remaining files</Button>}
                </div>
              )}
              <div className="flex flex-wrap gap-2">
                <Button size="sm" variant="secondary" onClick={() => navigate('files')}>
                  Open remote files
                </Button>
                <Button size="sm" variant="secondary" disabled={importing} onClick={() => picker.current?.click()}>
                  Import another directory
                </Button>
              </div>
            </>
          )}
          <ExclusionList entries={status.excluded} label="Left out before upload" />
          <ExclusionList entries={status.result.excluded} label="Server left out" />
        </div>
      )}
    </section>
  )
}
