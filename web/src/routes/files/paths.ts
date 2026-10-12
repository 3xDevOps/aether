export interface FileTarget {
  path: string
  line?: number
  column?: number
}

export interface PathMatch extends FileTarget {
  start: number
  end: number
}

/** Where every run's checkout is mounted inside its container. */
const mount = '/workspace/'

const token = /(?<![\w/.~:+-])(?!@)((?:\.?\/)?(?:[\w@.+-]+\/)*[\w@.+-]*\w)(?::(\d+)(?::(\d+))?|#L(\d+))?/g

function repositoryPath(raw: string, located: boolean): string | null {
  const path = raw.startsWith(mount) ? raw.slice(mount.length) : raw.startsWith('./') ? raw.slice(2) : raw
  const parts = path.split('/')
  if (parts.some((part) => part === '' || part === '.' || part === '..' || part === '.git')) return null
  if (!/[A-Za-z]/.test(path)) return null
  // A lone word names a file only with an extension or a line: `go.mod`, `Makefile:12`.
  if (parts.length === 1 && !located && !path.includes('.')) return null
  return path
}

/** What in text could name a file in a run's checkout: `path`, `path:line`,
 * `path:line:col` or `path#L12`, relative to the repository or under
 * /workspace. Whether the file exists is for the caller to ask. */
export function findPaths(text: string): PathMatch[] {
  const out: PathMatch[] = []
  for (const match of text.matchAll(token)) {
    const line = match[2] ?? match[4]
    const path = repositoryPath(match[1]!, line !== undefined)
    if (path === null) continue
    out.push({
      start: match.index,
      end: match.index + match[0].length,
      path,
      ...(line === undefined ? {} : { line: Number(line) }),
      ...(match[3] === undefined ? {} : { column: Number(match[3]) }),
    })
  }
  return out
}

/** The one file target that is all of text, as a terminal link or a Markdown href is. */
export function parsePath(text: string): FileTarget | null {
  const [match] = findPaths(text)
  if (!match || match.start !== 0 || match.end !== text.length) return null
  const { path, line, column } = match
  return { path, ...(line === undefined ? {} : { line }), ...(column === undefined ? {} : { column }) }
}
