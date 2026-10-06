import { useMemo, useState } from 'react'
import type * as React from 'react'
import { CircleCheck, Circle, CircleDot, ListTodo } from '@/components/icons'
import { cn, focusRingInset } from '@/lib/utils'
import { parsePatch } from '@/routes/diff/parse'

const planFold = 20

const planIcon: Record<string, React.ReactNode> = {
  completed: <CircleCheck aria-label="Done" className="text-muted" />,
  in_progress: <CircleDot aria-label="In progress" className="text-text" />,
}

export function PlanCard({ entries }: { entries: { content: string; status?: string }[] }) {
  const [open, setOpen] = useState(false)
  const shown = open ? entries : entries.slice(0, planFold)
  const done = entries.filter((entry) => entry.status === 'completed').length
  return (
    <section data-slot="plan-card" aria-label="Plan" className="flex flex-col gap-1 text-ui-sm">
      <div className="flex h-6 items-center gap-2 text-muted">
        <ListTodo aria-hidden className="size-3.5" />
        <span>Plan</span>
        <span className="tabular-nums">{done}/{entries.length}</span>
      </div>
      <ol className="flex flex-col">
        {shown.map((entry, index) => (
          <li key={index} className="flex min-h-6 items-start gap-2 pl-5 [&_svg]:mt-[5px] [&_svg]:size-3.5 [&_svg]:shrink-0">
            {planIcon[entry.status ?? ''] ?? <Circle aria-label="To do" className="text-icon-faint" />}
            <span className={cn('min-w-0 break-words', entry.status === 'completed' ? 'text-muted line-through' : 'text-text')}>
              {entry.content}
            </span>
          </li>
        ))}
      </ol>
      {entries.length > planFold && (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className={cn(focusRingInset, 'self-start rounded-control pl-5 text-accent hover:underline coarse:min-h-11')}
        >
          {open ? 'Show less' : `Show ${entries.length - planFold} more`}
        </button>
      )}
    </section>
  )
}

const lineClass = {
  add: 'bg-diff-add-bg',
  del: 'bg-diff-del-bg',
  hunk: 'text-muted',
  meta: 'text-muted',
  context: '',
}

const prefix = { add: '+', del: '-', hunk: '', meta: '', context: ' ' }

export function DiffBlock({ path, patch }: { path: string; patch: string }) {
  const file = useMemo(() => parsePatch(`diff --git a/${path} b/${path}\n${patch.replace(/\n+$/, '')}`)[0], [path, patch])
  if (!file) return null
  return (
    <figure data-slot="diff-block" className="overflow-hidden rounded-control border border-seam">
      <figcaption className="flex min-w-0 items-center gap-2 border-b border-seam bg-chrome px-2 py-0.5 text-ui-sm">
        <span className="min-w-0 flex-1 truncate font-code text-text">{file.path}</span>
        <span className="shrink-0 font-code text-diff-add">+{file.additions}</span>
        <span className="shrink-0 font-code text-diff-del">−{file.deletions}</span>
      </figcaption>
      <pre className="overflow-x-auto font-code text-ui-sm text-text">
        {file.lines.map((line, index) => (
          <code key={index} className={cn('block min-w-max px-2 whitespace-pre', lineClass[line.kind])}>
            {prefix[line.kind]}{line.text || ' '}
          </code>
        ))}
      </pre>
    </figure>
  )
}

const tailLines = 40

export function OutputTail({ output, exitCode }: { output: string; exitCode?: number }) {
  const lines = output.replace(/\n$/, '').split('\n')
  const cut = lines.length > tailLines
  return (
    <figure data-slot="output-tail" className="flex flex-col gap-0.5 text-ui-sm">
      <pre className="max-h-64 overflow-auto rounded-control border border-seam bg-chrome px-2 py-1 font-code whitespace-pre text-text">
        {cut && <span className="block text-muted">… {lines.length - tailLines} earlier lines</span>}
        {lines.slice(-tailLines).join('\n')}
      </pre>
      {exitCode !== undefined && (
        <figcaption className={cn('tabular-nums', exitCode === 0 ? 'text-muted' : 'text-state-failed')}>Exit code {exitCode}</figcaption>
      )}
    </figure>
  )
}

export function ChangedFiles({
  files,
  onOpen,
}: {
  files: { path: string; additions: number; deletions: number }[]
  onOpen: () => void
}) {
  return (
    <div data-slot="changed-files" className="flex flex-col text-ui-sm">
      <span className="flex h-6 items-center text-muted">
        Changed {files.length} {files.length === 1 ? 'file' : 'files'}
      </span>
      <ul>
        {files.map((file) => (
          <li key={file.path}>
            <button
              type="button"
              onClick={onOpen}
              className={cn(focusRingInset, 'flex h-6 w-full min-w-0 items-center gap-2 rounded-control pl-5 text-left hover:bg-hover coarse:h-11')}
            >
              <span className="min-w-0 flex-1 truncate font-code text-text">{file.path}</span>
              <span className="shrink-0 font-code text-diff-add tabular-nums">+{file.additions}</span>
              <span className="shrink-0 font-code text-diff-del tabular-nums">−{file.deletions}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}
