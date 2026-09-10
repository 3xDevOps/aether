import { readdir } from 'node:fs/promises'

export interface Tag {
  name: string
  attributes: string
}

/** Every file under `src` that is not itself a test. Tests are left out
 * because they name the classes and the tags they assert. */
export async function sourceFiles(): Promise<string[]> {
  const root = `${process.cwd()}/src`
  const files = await readdir(root, { recursive: true, withFileTypes: true })
  return files
    .filter((e) => e.isFile() && /\.(tsx?|css)$/.test(e.name) && !e.name.includes('.test.'))
    .map((e) => `${e.parentPath ?? root}/${e.name}`)
}

/**
 * Every opening tag of the named elements in one file, with its own
 * attributes and no other. Per file is not enough: one compliant tag would
 * let every tag beside it through, and the exemption widens as a file grows.
 *
 * A regex rather than a parser, so one of these names written inside a comment
 * or a string counts as a tag. That fails a scan rather than letting a real
 * one through, but it is why a comment naming an element it must not draw is
 * worth avoiding.
 */
export function openingTags(source: string, names: string[]): Tag[] {
  const tags: Tag[] = []
  // The lookahead consumes nothing, so a name is matched however the tag goes
  // on: whitespace, a slash, a `>`, or a generic argument.
  const opening = new RegExp(`<(${names.join('|')})(?![A-Za-z0-9.])`, 'g')
  for (const match of source.matchAll(opening)) {
    let end = match.index + match[0].length
    // A generic type argument sits between the name and the attributes, and
    // its own `>` closes nothing: `<Tooltip.Trigger<'button'> ...>`.
    if (source[end] === '<') end = source.indexOf('>', end) + 1
    let depth = 0
    let quoted = false
    while (end < source.length) {
      const c = source[end]
      // A `>` inside a JSX expression or a quoted attribute closes nothing,
      // and `[&>svg]:size-3` is ordinary Tailwind. Double quotes only: an
      // apostrophe in prose would open a string that never closes.
      if (c === '"') quoted = !quoted
      else if (!quoted && c === '{') depth += 1
      else if (!quoted && c === '}') depth -= 1
      else if (!quoted && depth === 0 && c === '>') break
      end += 1
    }
    tags.push({ name: match[1], attributes: source.slice(match.index, end) })
  }
  return tags
}
