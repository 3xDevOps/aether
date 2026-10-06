export type KeyScope = 'global' | 'run' | 'request' | 'composer'

/** Inner scopes first. A request card and the composer never hold focus at
 * once, so their relative order only settles a tie the table forbids. */
const depth: Record<KeyScope, number> = { composer: 0, request: 1, run: 2, global: 3 }

export interface ScopeEntry {
  scope: KeyScope
  /** Read at dispatch, so a handler that appears after mount (a permission
   * landing with hydration) is live without remounting. */
  handlers: { current: Partial<Record<string, (event: KeyboardEvent) => void>> }
}

const stack: ScopeEntry[] = []

export function pushScope(entry: ScopeEntry): () => void {
  stack.push(entry)
  return () => {
    const index = stack.lastIndexOf(entry)
    if (index !== -1) stack.splice(index, 1)
  }
}

/** Every entry on the stack, innermost scope first, the latest push first
 * within a scope. Mount order cannot decide this: React runs a child's
 * effects before its parent's, so a run view pushes before the shell. */
export function activeScopes(): ScopeEntry[] {
  return stack
    .map((entry, index) => ({ entry, index }))
    .sort((a, b) => depth[a.entry.scope] - depth[b.entry.scope] || b.index - a.index)
    .map(({ entry }) => entry)
}
