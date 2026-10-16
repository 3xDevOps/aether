// Unified diff text, split into the shape the view renders. The dashboard
// never edits code, so this is deliberately the whole of its diff support:
// no editor, no language grammar, just the line kinds a reader needs.

export type LineKind = 'add' | 'del' | 'context' | 'hunk' | 'meta'

export interface PatchLine {
  kind: LineKind
  text: string
  old?: number
  new?: number
}

export const linePrefix: Record<LineKind, string> = { add: '+', del: '-', hunk: '', meta: '', context: ' ' }

export type FileStatus = 'added' | 'deleted' | 'modified' | 'binary'

export interface PatchFile {
  path: string
  status: FileStatus
  additions: number
  deletions: number
  /** Added, removed and context lines: everything except hunk headers and meta lines. */
  contentLines: number
  /** Length of `text`, in characters. */
  size: number
  /** This file's slice of the patch, from its `diff --git` line. */
  text: string
  /** Parsed on first read and kept, so a file nobody reads is never split into lines. */
  readonly lines: PatchLine[]
}

const fileHeader = 'diff --git '
const boundary = `\n${fileHeader}`
const hunkStart = /^@@ -(\d+)(?:,\d+)? \+(\d+)/

type Row = LineKind | 'preamble'

/**
 * Splits `git diff` output into one entry per file with its counts, without
 * splitting the files into lines: those are parsed when `lines` is first read.
 * A file in `previous` whose text is unchanged is returned as the same object,
 * so what is memoised on it survives a refetch. Parsing is deliberately
 * tolerant of incomplete input so a transport or Git error cannot discard
 * useful lines that arrived before it.
 */
export function parsePatch(text: string, previous: readonly PatchFile[] = []): PatchFile[] {
  const known = new Map(previous.map((file) => [firstLine(file.text), file]))
  const files: PatchFile[] = []
  let start = text.startsWith(fileHeader) ? 0 : nextFile(text, 0)
  while (start >= 0) {
    const next = nextFile(text, start)
    const slice = text.slice(start, next < 0 ? text.length : next)
    const prior = known.get(firstLine(slice))
    files.push(prior?.text === slice ? prior : readFile(slice))
    start = next
  }
  return files
}

function nextFile(text: string, from: number): number {
  const at = text.indexOf(boundary, from)
  return at < 0 ? -1 : at + 1
}

function firstLine(text: string): string {
  const end = text.indexOf('\n')
  return end < 0 ? text : text.slice(0, end)
}

/**
 * Calls `visit` with the kind and extent of every line from `from`. Inside a
 * hunk every line means what its first character says, so the preamble has to
 * be recognised there and only there: a deleted SQL comment is
 * "--- something" and is not a file marker.
 */
function walk(text: string, from: number, visit: (row: Row, start: number, end: number) => void): void {
  let inHunk = false
  for (let start = from; start < text.length; ) {
    let end = text.indexOf('\n', start)
    if (end < 0) end = text.length
    if (text.startsWith('@@', start)) {
      inHunk = true
      visit('hunk', start, end)
    } else if (!inHunk) {
      visit('preamble', start, end)
    } else if (end > start) {
      const first = text.charCodeAt(start)
      visit(first === 43 ? 'add' : first === 45 ? 'del' : first === 92 ? 'meta' : 'context', start, end)
    }
    start = end + 1
  }
}

function readFile(text: string): PatchFile {
  const headerEnd = text.indexOf('\n')
  const body = headerEnd < 0 ? text.length : headerEnd + 1
  let path = headerPath(firstLine(text))
  let status: FileStatus = 'modified'
  let additions = 0
  let deletions = 0
  let content = 0
  walk(text, body, (row, start, end) => {
    if (row === 'add') {
      additions++
      content++
    } else if (row === 'del') {
      deletions++
      content++
    } else if (row === 'context') {
      content++
    } else if (row === 'preamble') {
      // The +++ line is the authority on the path: it alone survives a name
      // git had to quote in the `diff --git` header.
      const marker = text.startsWith('+++ ', start) ? markerPath(text.slice(start, end)) : ''
      if (marker) path = marker
      else if (text.startsWith('new file mode', start)) status = 'added'
      else if (text.startsWith('deleted file mode', start)) status = 'deleted'
      else if (text.startsWith('Binary files', start)) status = 'binary'
    }
  })
  let lines: PatchLine[] | undefined
  return {
    path,
    status,
    additions,
    deletions,
    contentLines: content,
    size: text.length,
    text,
    get lines() {
      return (lines ??= readLines(text, body))
    },
  }
}

function readLines(text: string, from: number): PatchLine[] {
  const lines: PatchLine[] = []
  let oldLine = 0
  let newLine = 0
  walk(text, from, (row, start, end) => {
    if (row === 'hunk') {
      const line = text.slice(start, end)
      const numbers = hunkStart.exec(line)
      oldLine = Number(numbers?.[1] ?? 0)
      newLine = Number(numbers?.[2] ?? 0)
      lines.push({ kind: 'hunk', text: line })
    } else if (row === 'preamble') {
      if (text.startsWith('Binary files', start)) lines.push({ kind: 'meta', text: text.slice(start, end) })
    } else if (row === 'add') {
      lines.push({ kind: 'add', text: text.slice(start + 1, end), new: newLine++ })
    } else if (row === 'del') {
      lines.push({ kind: 'del', text: text.slice(start + 1, end), old: oldLine++ })
    } else if (row === 'meta') {
      lines.push({ kind: 'meta', text: text.slice(start, end) })
    } else {
      lines.push({ kind: 'context', text: text.slice(start + 1, end), old: oldLine++, new: newLine++ })
    }
  })
  return lines
}

/** `diff --git a/x/y b/x/y` -> `x/y`, best effort for paths with spaces. */
function headerPath(line: string): string {
  const rest = line.slice('diff --git '.length)
  const half = rest.indexOf(' b/')
  return unquote(half > 0 ? rest.slice(half + 3) : rest.replace(/^a\//, ''))
}

/** `+++ b/x/y` -> `x/y`; a deletion points at `/dev/null` and keeps the
 * path the `diff --git` header gave it. */
function markerPath(line: string): string {
  const rest = line.slice(4).split('\t')[0]
  if (rest === '/dev/null') return ''
  return unquote(rest.replace(/^b\//, ''))
}

function unquote(path: string): string {
  return path.startsWith('"') && path.endsWith('"') ? path.slice(1, -1) : path
}
