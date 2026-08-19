// Unified diff text, split into the shape the view renders. The dashboard
// never edits code, so this is deliberately the whole of its diff support:
// no editor, no language grammar, just the line kinds a reader needs.

export type LineKind = 'add' | 'del' | 'context' | 'hunk' | 'meta'

export interface PatchLine {
  kind: LineKind
  text: string
}

export type FileStatus = 'added' | 'deleted' | 'modified' | 'binary'

export interface PatchFile {
  path: string
  status: FileStatus
  additions: number
  deletions: number
  lines: PatchLine[]
}

const fileHeader = /^diff --git /

/**
 * Splits `git diff` output into one entry per file. A truncated patch ends
 * mid-file, which parses to a file with fewer lines rather than an error -
 * the view says the diff was cut short, and everything above the cut still
 * reads.
 */
export function parsePatch(text: string): PatchFile[] {
  const files: PatchFile[] = []
  let file: PatchFile | null = null
  // Inside a hunk every line means what its first character says, so the
  // preamble has to be recognised there and only there: a deleted SQL
  // comment is "--- something" and is not a file marker.
  let inHunk = false

  for (const line of text.split('\n')) {
    if (fileHeader.test(line)) {
      file = { path: headerPath(line), status: 'modified', additions: 0, deletions: 0, lines: [] }
      files.push(file)
      inHunk = false
      continue
    }
    if (!file) continue

    if (line.startsWith('@@')) {
      inHunk = true
      file.lines.push({ kind: 'hunk', text: line })
      continue
    }
    if (!inHunk) {
      // The +++ line is the authority on the path: it alone survives a name
      // git had to quote in the `diff --git` header.
      const marker = markerPath(line)
      if (line.startsWith('+++ ') && marker) file.path = marker
      else if (line.startsWith('new file mode')) file.status = 'added'
      else if (line.startsWith('deleted file mode')) file.status = 'deleted'
      else if (line.startsWith('Binary files')) {
        file.status = 'binary'
        file.lines.push({ kind: 'meta', text: line })
      }
      continue
    }
    if (line.startsWith('+')) {
      file.additions++
      file.lines.push({ kind: 'add', text: line.slice(1) })
    } else if (line.startsWith('-')) {
      file.deletions++
      file.lines.push({ kind: 'del', text: line.slice(1) })
    } else if (line.startsWith('\\')) {
      file.lines.push({ kind: 'meta', text: line })
    } else {
      file.lines.push({ kind: 'context', text: line.slice(1) })
    }
  }
  return files
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
