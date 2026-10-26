import { useMemo, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { CircleCheck } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { CodeBlock } from '@/components/ui/code'
import { CopyButton } from '@/components/ui/copy-button'
import { Highlighted } from '@/components/ui/markdown'
import { StatusDot } from '@/components/ui/status-dot'
import { cn, focusRingInset } from '@/lib/utils'
import { parsePatch, type PatchFile } from '@/routes/diff/parse'
import { PathText } from '@/routes/files/path-link'
import type { PlanEntry } from '@/store/session-steps'

const planFold = 20

export function PlanEntries({ entries, live }: { entries: PlanEntry[]; live: boolean }) {
  const [open, setOpen] = useState(false)
  const shown = open ? entries : entries.slice(0, planFold)
  const done = entries.filter((entry) => entry.status === 'completed').length
  return (
    <div data-slot="plan-entries" className="flex flex-col pb-1 text-ui-sm">
      <ol aria-label="Plan" className="flex flex-col">
        {shown.map((entry, index) => (
          <li key={index} className="flex min-h-6 items-start gap-2">
            <span className="flex h-5 w-3.5 shrink-0 items-center justify-center">
              {entry.status === 'completed' ? <CircleCheck aria-label="Done" className="size-3.5 text-icon-faint" />
                : entry.status === 'in_progress' ? <StatusDot tone="working" pulse={live} label="In progress" />
                : <span role="img" aria-label="To do" className="size-1.5 rounded-full border border-icon-faint" />}
            </span>
            <span className={cn('min-w-0 py-0.5 break-words', entry.status === 'completed' ? 'text-muted' : entry.status === 'in_progress' && live ? 'live-shimmer' : 'text-text')}>
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
          className={cn(focusRingInset, 'self-start rounded-control pl-5.5 text-accent hover:underline coarse:min-h-11')}
        >
          {open ? 'Show less' : `Show ${entries.length - planFold} more`}
        </button>
      )}
      {entries.length > 0 && done === entries.length && (
        <p className="flex min-h-6 items-center gap-2 text-state-done">
          <span className="flex w-3.5 shrink-0 justify-center"><StatusDot tone="done" /></span>
          All {entries.length} {entries.length === 1 ? 'step' : 'steps'} done
        </p>
      )}
    </div>
  )
}

const clipLines = 10

export function ToolValue({ label, value, failed = false }: { label: string; value: unknown; failed?: boolean }) {
  const [all, setAll] = useState(false)
  const code = useRef<HTMLElement>(null)
  const json = typeof value !== 'string'
  const text = useMemo(() => (json ? JSON.stringify(value, null, 2) : value), [json, value])
  const lines = text.split('\n')
  const clipped = !all && lines.length > clipLines
  const shown = clipped ? lines.slice(0, clipLines).join('\n') : text
  return (
    <figure data-slot="tool-value" className="flex flex-col gap-0.5">
      <figcaption className="flex h-6 items-center justify-between text-ui-sm text-muted coarse:h-11">
        {label}
        <CopyButton text={text} label={`Copy the ${label.toLowerCase()}`} source={code} reveal={() => flushSync(() => setAll(true))} />
      </figcaption>
      <CodeBlock className={cn('max-h-80 overflow-auto px-2 py-1', failed && 'border-state-failed bg-state-failed-soft')}>
        <code ref={code}>{json ? <Highlighted code={shown} lang="json" /> : shown}</code>
      </CodeBlock>
      {clipped && (
        <Button variant="quiet" className="self-start text-ui-sm" onClick={() => setAll(true)}>
          Show all {lines.length} lines
        </Button>
      )}
    </figure>
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

/** One file's tool-call patch, which arrives without its `diff --git` header. */
export function patchFile(path: string, patch: string) {
  return parsePatch(`diff --git a/${path} b/${path}\n${patch.replace(/\n+$/, '')}`)[0]
}

export function DiffLines({ file }: { file: PatchFile }) {
  return (
    <pre className="overflow-x-auto font-code text-ui-sm text-text">
      {file.lines.map((line, index) => (
        <code key={index} className={cn('block min-w-max px-2 whitespace-pre', lineClass[line.kind])}>
          {prefix[line.kind]}{line.text || ' '}
        </code>
      ))}
    </pre>
  )
}

export function DiffBlock({ path, patch }: { path: string; patch: string }) {
  const file = useMemo(() => patchFile(path, patch), [path, patch])
  if (!file) return null
  return (
    <figure data-slot="diff-block" className="overflow-hidden rounded-control border border-seam">
      <figcaption className="flex min-w-0 items-center gap-2 border-b border-seam bg-chrome px-2 py-0.5 text-ui-sm">
        <span className="min-w-0 flex-1 truncate font-code text-text"><PathText>{file.path}</PathText></span>
        <span className="shrink-0 font-code text-diff-add">+{file.additions}</span>
        <span className="shrink-0 font-code text-diff-del">−{file.deletions}</span>
      </figcaption>
      <DiffLines file={file} />
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
        <PathText>{lines.slice(-tailLines).join('\n')}</PathText>
      </pre>
      {exitCode !== undefined && (
        <figcaption className={cn('tabular-nums', exitCode === 0 ? 'text-muted' : 'text-state-failed')}>Exit code {exitCode}</figcaption>
      )}
    </figure>
  )
}
