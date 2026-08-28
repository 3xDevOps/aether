// The pure edit behind the review gate's per-item remove toggle: dropping
// a manifest item drops the Dockerfile lines it maps to. Span semantics
// mirror internal/envdef exactly: 1-based, inclusive.

import type { ManifestItem } from '@/lib/types'

/** A Dockerfile and manifest pair after an edit. */
export interface ManifestEdit {
  dockerfile: string
  items: ManifestItem[]
}

/**
 * removeManifestItem drops the named item and the Dockerfile lines its
 * span maps to, shifting every later span up by the removed line count.
 * Returns null when the item is the last one left: the review gate offers
 * the standard environment instead of an empty Dockerfile. Throws when
 * the name is not in the manifest or its span runs past the file - both
 * are contract violations, not user actions.
 */
export function removeManifestItem(
  dockerfile: string,
  items: ManifestItem[],
  name: string,
): ManifestEdit | null {
  const at = items.findIndex((item) => item.name === name)
  if (at < 0) {
    throw new Error(`removeManifestItem: no manifest item named ${JSON.stringify(name)}`)
  }
  if (items.length === 1) return null

  const removed = items[at]
  const trailing = dockerfile.endsWith('\n')
  const lines = (trailing ? dockerfile.slice(0, -1) : dockerfile).split('\n')
  if (removed.start_line < 1 || removed.end_line > lines.length) {
    throw new Error(
      `removeManifestItem: item ${JSON.stringify(name)} spans lines ` +
        `${removed.start_line}-${removed.end_line}, outside the Dockerfile's ${lines.length} lines`,
    )
  }

  const count = removed.end_line - removed.start_line + 1
  const kept = lines.filter(
    (_, i) => i + 1 < removed.start_line || i + 1 > removed.end_line,
  )
  const shifted = items
    .filter((_, i) => i !== at)
    .map((item) =>
      item.start_line > removed.end_line
        ? {
            ...item,
            start_line: item.start_line - count,
            end_line: item.end_line - count,
          }
        : item,
    )
  return { dockerfile: kept.join('\n') + (trailing ? '\n' : ''), items: shifted }
}
