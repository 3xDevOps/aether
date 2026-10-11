import { createContext, type ReactNode, useContext, useMemo, useRef } from 'react'
import type { Run } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
import { hasFiles, isListedFile, isRunFile, openInFiles } from '@/routes/files/open'
import { findPaths, type FileTarget } from '@/routes/files/paths'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

type Checkout = Pick<Run, 'id' | 'workspace_id'>

const CheckoutContext = createContext<Checkout | null>(null)

/** Inside it, text that names a file in this run's checkout opens that file
 * in the run's Files view. */
export function RunFileLinks({ run, children }: { run: Run; children: ReactNode }) {
  const linked = hasFiles(run, useCapability())
  const checkout = useMemo(
    () => (linked ? { id: run.id, workspace_id: run.workspace_id } : null),
    [linked, run.id, run.workspace_id],
  )
  return <CheckoutContext.Provider value={checkout}>{children}</CheckoutContext.Provider>
}

/**
 * One path. It is a link once a directory listing says the file exists;
 * pointing at it, or tapping it, reads that listing. Nothing is asked of the
 * server while the text is only on screen. An explicit one, which the agent
 * wrote as a Markdown link, is a link without asking.
 */
export function FileLink({ target, explicit = false, children }: { target: FileTarget; explicit?: boolean; children: ReactNode }) {
  const run = useContext(CheckoutContext)
  const listed = useStore((s) => run !== null && isListedFile(s.trees, run, target.path))
  // A change to the checkout drops its listings. A path that was a file stays
  // a link rather than losing its underline under the pointer.
  const known = useRef<string | null>(null)
  if (listed) known.current = target.path
  const file = explicit || known.current === target.path
  if (!run) return children
  const open = () => {
    // Dragging across a path selects it; only a plain click opens it.
    if (window.getSelection()?.isCollapsed === false) return
    if (file) openInFiles(run.id, target)
    else void isRunFile(run, target.path).then((found) => found && openInFiles(run.id, target), () => {})
  }
  return (
    <span
      role={file ? 'link' : undefined}
      tabIndex={file ? 0 : undefined}
      className={file ? cn(focusRing, 'cursor-pointer rounded-control text-accent underline-offset-2 hover:underline') : undefined}
      onPointerEnter={file ? undefined : () => void isRunFile(run, target.path).catch(() => {})}
      onClick={open}
      onKeyDown={(event) => {
        if (file && event.key === 'Enter') open()
      }}
    >
      {children}
    </span>
  )
}

/** Text with each path it names as a FileLink. */
export function PathText({ children }: { children: string }) {
  const linked = useContext(CheckoutContext) !== null
  const matches = useMemo(() => (linked ? findPaths(children) : []), [linked, children])
  if (matches.length === 0) return children
  const out: ReactNode[] = []
  let at = 0
  for (const match of matches) {
    out.push(
      children.slice(at, match.start),
      <FileLink key={match.start} target={match}>{children.slice(match.start, match.end)}</FileLink>,
    )
    at = match.end
  }
  out.push(children.slice(at))
  return out
}
