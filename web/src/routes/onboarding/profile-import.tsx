// Part B of the onboarding Agents step: an explicit, one-time import of a
// configuration directory. The browser reads the chosen directory; the
// server remains responsible for validation and secret scanning. Nothing here
// watches the local directory or depends on a local gateway capability.

import { type ChangeEvent, useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import type { Api } from '@/lib/api'
import { friendly, formatBytes, message } from '@/lib/format'
import type {
  ConfigExclusion,
  ConfigFile,
  ConfigImportResult,
  ConfigRoot,
} from '@/lib/types'
import { useStore } from '@/store'

export const MAX_IMPORT_FILES = 2000
export const MAX_IMPORT_FILE_BYTES = 1024 * 1024
export const MAX_IMPORT_TOTAL_BYTES = 20 * 1024 * 1024

const credentialNames = new Set([
  '.credentials.json',
  'credentials.json',
  'credentials',
  '.claude.json',
  'auth.json',
  'keychain',
  'token.json',
  'tokens.json',
  'oauth.json',
])

const runtimePaths = [
  'projects',
  'shell-snapshots',
  'statsig',
  'todos',
  'file-history',
  'history.jsonl',
  'daemon',
  'tmp',
  '.tmp',
  'sessions',
]

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
  files: ConfigFile[]
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

function isCredential(path: string): boolean {
  const parts = path.split('/').map((part) => part.toLowerCase())
  return parts.some((part) => credentialNames.has(part) || part.endsWith('.pem'))
}

function isRuntime(path: string): boolean {
  const normalized = path.toLowerCase()
  return runtimePaths.some(
    (entry) => normalized === entry || normalized.startsWith(`${entry}/`),
  )
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

/**
 * Reads only files that fit the import limits and builds the byte-preserving
 * payload sent to config.import. It deliberately does not decode text: an
 * empty file stays empty and arbitrary bytes stay arbitrary bytes. Callers
 * should discard the result when their selection generation is stale.
 */
export async function prepareDirectoryImport(
  selected: File[] | FileList,
  generationIsCurrent: () => boolean = () => true,
): Promise<Selection | null> {
  const files = Array.from(selected)
  if (files.length === 0) return null
  const first = relativePath(files[0])
  if (!first.valid) return null
  const basename = first.root
  const excluded: LocalExclusion[] = []
  const payload: ConfigFile[] = []
  const paths = new Set<string>()
  let bytes = 0

  for (const file of files) {
    if (!generationIsCurrent()) return null
    const parts = relativePath(file)
    const displayPath = parts.valid ? parts.path : file.name
    if (!parts.valid || parts.root !== basename) {
      excluded.push(
        exclusion(
          displayPath,
          'invalid-path',
          'path is not a file below the selected directory',
        ),
      )
      continue
    }
    if (paths.has(parts.path)) {
      excluded.push(exclusion(parts.path, 'duplicate', 'duplicate path selected'))
      continue
    }
    paths.add(parts.path)
    if (isCredential(parts.path)) {
      excluded.push(
        exclusion(parts.path, 'credential', 'credential file excluded before upload'),
      )
      continue
    }
    if (isRuntime(parts.path)) {
      excluded.push(
        exclusion(parts.path, 'runtime', 'runtime history excluded before upload'),
      )
      continue
    }
    if (file.size > MAX_IMPORT_FILE_BYTES) {
      excluded.push(
        exclusion(
          parts.path,
          'too-large',
          `file is larger than ${formatBytes(MAX_IMPORT_FILE_BYTES)} and was not read`,
        ),
      )
      continue
    }
    if (payload.length >= MAX_IMPORT_FILES) {
      excluded.push(
        exclusion(
          parts.path,
          'too-many',
          `the import is limited to ${MAX_IMPORT_FILES} files`,
        ),
      )
      continue
    }
    if (bytes + file.size > MAX_IMPORT_TOTAL_BYTES) {
      excluded.push(
        exclusion(
          parts.path,
          'over-budget',
          `the import is limited to ${formatBytes(MAX_IMPORT_TOTAL_BYTES)}`,
        ),
      )
      continue
    }

    const content = await readBytes(file)
    if (!generationIsCurrent()) return null
    // A file can change while the chooser is open. Do not retain a read that
    // crossed either bound even if its File.size was small when selected.
    if (content.byteLength > MAX_IMPORT_FILE_BYTES) {
      excluded.push(
        exclusion(
          parts.path,
          'too-large',
          `file is larger than ${formatBytes(MAX_IMPORT_FILE_BYTES)} and was not uploaded`,
        ),
      )
      continue
    }
    if (bytes + content.byteLength > MAX_IMPORT_TOTAL_BYTES) {
      excluded.push(
        exclusion(
          parts.path,
          'over-budget',
          `the import is limited to ${formatBytes(MAX_IMPORT_TOTAL_BYTES)}`,
        ),
      )
      continue
    }
    payload.push({ path: parts.path, content_base64: base64(content), mode: 0o644 })
    bytes += content.byteLength
  }

  return { basename, files: payload, bytes, excluded }
}

function ExclusionList({ entries, label }: { entries: ConfigExclusion[]; label: string }) {
  if (entries.length === 0) return null
  const shown = entries.slice(0, 50)
  return (
    <div className="space-y-2 border-t border-border/70 pt-2">
      <p className="text-sm font-medium">{label}: {entries.length}</p>
      <ul className="max-h-52 min-w-0 space-y-1 overflow-y-auto text-xs">
        {shown.map((entry, index) => (
          <li key={`${entry.path}-${index}`}>
            <span className="font-mono">{entry.path}</span>
            <span className="text-muted-foreground">
              {' '}— {entry.detail ? `${entry.reason}: ${entry.detail}` : entry.reason}
            </span>
          </li>
        ))}
      </ul>
      {entries.length > shown.length && (
        <p className="text-xs text-muted-foreground">
          {entries.length - shown.length} more exclusions are not shown.
        </p>
      )}
    </div>
  )
}

export function ProfileImport({ client }: { client: Api }) {
  const navigate = useStore((state) => state.navigate)
  const [roots, setRoots] = useState<ConfigRoot[] | null>(null)
  const [rootsError, setRootsError] = useState<string | null>(null)
  const [selection, setSelection] = useState<Selection | null>(null)
  const [selectedHarness, setSelectedHarness] = useState('')
  const [reading, setReading] = useState(false)
  const [selectionError, setSelectionError] = useState<string | null>(null)
  const [result, setResult] = useState<ConfigImportResult | null>(null)
  const [importError, setImportError] = useState<string | null>(null)
  const importing = useStore((state) => state.onboardingImportPending)
  const picker = useRef<HTMLInputElement | null>(null)
  const generation = useRef(0)

  useEffect(() => {
    let active = true
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

  useEffect(() => {
    if (!selection || roots === null) return
    const matching = roots.filter((root) => rootName(root.path) === selection.basename)
    setSelectedHarness((current) => {
      if (current && roots.some((root) => root.harness === current)) return current
      return matching.length === 1 ? matching[0].harness : ''
    })
  }, [roots, selection])

  const matchingRoots = selection
    ? (roots ?? []).filter((root) => rootName(root.path) === selection.basename)
    : []
  const destinationRoots =
    matchingRoots.length > 0 ? matchingRoots : (roots ?? [])
  const destination = destinationRoots.find(
    (root) => root.harness === selectedHarness,
  )
  const friendlyDestination = destination
    ? friendly[destination.harness] ?? destination.harness
    : ''
  async function choose(event: ChangeEvent<HTMLInputElement>) {
    if (useStore.getState().onboardingImportPending) return
    const selected = Array.from(event.currentTarget.files ?? [])
    const version = ++generation.current
    setSelection(null)
    setSelectedHarness('')
    setSelectionError(null)
    setImportError(null)
    setResult(null)
    event.currentTarget.value = ''
    if (selected.length === 0) return
    const first = relativePath(selected[0])
    if (!first.valid) {
      setSelectionError('Choose a directory, not an individual file.')
      return
    }
    setReading(true)
    try {
      const prepared = await prepareDirectoryImport(
        selected,
        () => generation.current === version,
      )
      if (generation.current !== version) return
      if (prepared) setSelection(prepared)
    } catch (err) {
      if (generation.current === version) setSelectionError(message(err))
    } finally {
      if (generation.current === version) setReading(false)
    }
  }

  async function importConfiguration() {
    if (!selection || !destination || result || useStore.getState().onboardingImportPending) return
    const version = generation.current
    const harness = destination.harness
    const files = selection.files
    useStore.setState({ onboardingImportPending: true })
    setImportError(null)
    try {
      const imported = await client.configImport({ harness, files })
      if (generation.current === version) setResult(imported)
    } catch (err) {
      if (generation.current === version) {
        const detail = message(err)
        setImportError(
          `Import outcome is unknown: ${detail}. Some files may have been copied; inspect Files before retrying.`,
        )
      }
    } finally {
      useStore.setState({ onboardingImportPending: false })
    }
  }
  const rootLabel = selection?.basename || 'your agent configuration directory'

  return (
    <section
      aria-label="Bring your configuration"
      className="min-w-0 space-y-4 border-t border-border/70 py-3"
    >
      <div className="space-y-1">
        <h3 className="text-base font-semibold">Bring your configuration</h3>
        <p className="text-sm leading-6 text-muted-foreground">
          Choose one agent configuration directory to import once. Supported
          roots include <span className="font-mono">~/.claude</span>,{' '}
          <span className="font-mono">~/.codex</span>, and{' '}
          <span className="font-mono">~/.pi</span>.
        </p>
      </div>
      {importing && !selection && (
        <p role="status" className="text-sm text-muted-foreground">Importing configuration…</p>
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
            onChange={(event) => void choose(event)}
          />
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={importing}
            onClick={() => picker.current?.click()}
          >
            Choose directory
          </Button>
        </div>
        <p className="mt-2 text-[13px] leading-5 text-muted-foreground">
          Desktop browsers provide the directory contents directly. Known
          credential files and runtime history are omitted locally; remaining
          files are sent to the server and checked before writing. Files are
          limited to 1 MiB each, 20 MiB total, and {MAX_IMPORT_FILES} files.
        </p>
      </div>

      {reading && (
        <p className="border-l-2 border-state-working/60 bg-state-working/5 px-3 py-2 text-sm" role="status">
          Reading {rootLabel}; files over the limits and known runtime files are left out...
        </p>
      )}
      {selectionError && (
        <p className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-sm text-state-failed" role="alert">
          {selectionError}
        </p>
      )}
      {selection && !reading && (
        <div className="min-w-0 space-y-3 border-y border-border/70 bg-card px-3 py-3">
          <div className="space-y-1">
            <h4 className="text-sm font-semibold">Preview</h4>
            <p className="text-sm">
              {selection.files.length} files, {formatBytes(selection.bytes)} ready from{' '}
              <span className="font-mono">{selection.basename}</span>.
            </p>
            <p className="text-[13px] leading-5 text-muted-foreground">
              Empty files are preserved. Review the omitted paths below before
              importing.
            </p>
          </div>

          <ExclusionList entries={selection.excluded} label="Left out before upload" />

          {roots !== null && roots.length > 0 && (
            <label className="flex min-w-0 flex-wrap items-center gap-3 text-sm">
              <span className="font-medium">
                {matchingRoots.length > 1 || matchingRoots.length === 0
                  ? 'Choose destination'
                  : 'Destination'}
              </span>
              {matchingRoots.length === 1 ? (
                <span className="font-mono text-xs text-muted-foreground">
                  {friendlyDestination || matchingRoots[0].harness} ({matchingRoots[0].path})
                </span>
              ) : (
                <select
                  aria-label="Configuration destination"
                  className="h-7 min-w-44 rounded-sm border border-input bg-background px-2 text-sm"
                  value={selectedHarness}
                  onChange={(event) => {
                    setSelectedHarness(event.target.value)
                    setImportError(null)
                  }}
                >
                  <option value="">Select an agent</option>
                  {destinationRoots.map((root) => (
                    <option key={root.harness} value={root.harness}>
                      {friendly[root.harness] ?? root.harness} ({root.path})
                    </option>
                  ))}
                </select>
              )}
            </label>
          )}
          <p className="border-l-2 border-state-working/60 bg-state-working/5 px-3 py-2 text-[13px] leading-5">
            Before you confirm: known credential files and runtime history are
            excluded, matching remote configuration files will be overwritten,
            accepted files change your persistent remote home immediately, and
            this local directory will not be watched.
          </p>
          {importError && (
            <div className="flex min-w-0 flex-wrap items-center gap-3 border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2">
              <p className="text-sm text-state-failed" role="alert">
                {importError}
              </p>
              <Button
                size="sm"
                variant="outline"
                onClick={() => navigate('files')}
              >
                Inspect Files
              </Button>
            </div>
          )}
          {result ? (
            result.error ? (
              <>
                <div className="space-y-2 border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-sm text-state-failed" role="alert">
                  <p>
                    Import incomplete: {result.files} files ({formatBytes(result.bytes)}) imported into{' '}
                    {friendly[result.harness] ?? result.harness}.
                  </p>
                  <p>{result.error}</p>
                  <p>
                    Copied files remain. Inspect Files before choosing the
                    directory again to retry.
                  </p>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => navigate('files')}
                  >
                    Inspect Files
                  </Button>
                </div>
                {result.imported_paths && (
                  <div className="space-y-2 border-t border-border/70 pt-2">
                    <p className="text-sm font-medium">Imported paths: {result.imported_paths.length}</p>
                    <ul className="max-h-52 min-w-0 space-y-1 overflow-y-auto text-xs">
                      {result.imported_paths.map((path) => (
                        <li key={path} className="font-mono">{path}</li>
                      ))}
                    </ul>
                  </div>
                )}
                <ExclusionList entries={result.excluded} label="Server left out" />
              </>
            ) : (
              <>
                <p className="border-l-2 border-state-done/60 bg-state-done/5 px-3 py-2 text-sm text-state-done">
                  Imported {result.files} files ({formatBytes(result.bytes)}) into{' '}
                  {friendly[result.harness] ?? result.harness}.
                </p>
                <ExclusionList entries={result.excluded} label="Server left out" />
              </>
            )
          ) : (
            <Button
              size="sm"
              disabled={importing || selection.files.length === 0 || !destination}
              onClick={() => void importConfiguration()}
            >
              {importing ? 'Importing...' : 'Import configuration'}
            </Button>
          )}
        </div>
      )}
    </section>
  )
}
