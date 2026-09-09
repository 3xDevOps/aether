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
  for (const match of source.matchAll(new RegExp(`<(${names.join('|')})[\\s/>]`, 'g'))) {
    let depth = 0
    let end = match.index + match[0].length
    // A tag with no attributes ends on the `>` the match itself consumed.
    // Reading on from there would swallow the child element's attributes.
    if (source[end - 1] === '>') end -= 1
    // JSX puts expressions in braces, and a `>` inside one closes nothing.
    while (end < source.length && (depth > 0 || source[end] !== '>')) {
      if (source[end] === '{') depth += 1
      if (source[end] === '}') depth -= 1
      end += 1
    }
    tags.push({ name: match[1], attributes: source.slice(match.index, end) })
  }
  return tags
}
