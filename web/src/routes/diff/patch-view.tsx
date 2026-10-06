import { memo } from 'react'
import { VList } from 'virtua'
import { ChevronDown, ChevronRight } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { FileStatus, PatchFile, PatchLine } from '@/routes/diff/parse'

export const largeFile = 500

const statusWord: Record<FileStatus, string> = {
  added: 'added',
  deleted: 'deleted',
  modified: '',
  binary: 'binary',
}

const lineTint = {
  add: 'bg-diff-add-bg',
  del: 'bg-diff-del-bg',
  hunk: 'bg-chrome text-muted',
  meta: 'text-muted',
  context: '',
}

const prefix = { add: '+', del: '-', hunk: '', meta: '', context: ' ' }

export function Counts({ additions, deletions }: { additions: number; deletions: number }) {
  return (
    <span className="shrink-0 tabular-nums">
      <span className="text-diff-add">+{additions}</span> <span className="text-diff-del">−{deletions}</span>
    </span>
  )
}

/** Without `onCollapsedChange` the file is always open and has no chevron. */
export const FilePatch = memo(function FilePatch({
  file,
  wrap,
  lineNumbers = false,
  collapsed = false,
  onCollapsedChange,
  onOpen,
  id,
}: {
  file: PatchFile
  wrap: boolean
  lineNumbers?: boolean
  collapsed?: boolean
  onCollapsedChange?: (path: string, collapsed: boolean) => void
  onOpen?: (path: string) => void
  id?: string
}) {
  const open = !onCollapsedChange || !collapsed
  const body = id ? `${id}-body` : undefined
  const gutter = lineNumbers ? `calc(${Math.max(String(lastLine(file)).length, 2)}ch + 1rem)` : undefined
  const size = file.lines.filter((line) => line.kind !== 'hunk' && line.kind !== 'meta').length
  return (
    <section id={id} aria-label={file.path} className="border-b border-seam">
      <header className="sticky top-0 z-10 flex h-8 min-w-0 items-center gap-2 border-b border-seam bg-chrome px-2 text-ui-sm coarse:h-11">
        {onCollapsedChange && (
          <Button
            variant="ghost"
            size="icon-sm"
            label={open ? `Collapse ${file.path}` : `Expand ${file.path}`}
            aria-expanded={open}
            aria-controls={body}
            onClick={() => onCollapsedChange(file.path, open)}
          >
            {open ? <ChevronDown /> : <ChevronRight />}
          </Button>
        )}
        {onOpen ? (
          <Button variant="ghost" size="sm" hint="Open in Files" onClick={() => onOpen(file.path)} className="min-w-0 shrink">
            <span className="truncate font-code text-text">{file.path}</span>
          </Button>
        ) : (
          <span className="min-w-0 truncate px-2 font-code text-text coarse:px-3" title={file.path}>
            {file.path}
          </span>
        )}
        {statusWord[file.status] && <span className="shrink-0 text-muted">{statusWord[file.status]}</span>}
        <span className="ml-auto flex shrink-0 items-center gap-3">
          {!open && file.lines.length > largeFile && (
            <span className="text-muted tabular-nums">{size} lines</span>
          )}
          <Counts additions={file.additions} deletions={file.deletions} />
        </span>
      </header>
      {open && (
        <div id={body} className={cn('font-code text-ui-sm', !wrap && 'overflow-x-auto overscroll-x-contain')}>
          {file.lines.length > largeFile ? (
            <VList data-slot="patch-lines" style={{ height: 'min(70dvh, 40rem)' }} data={file.lines}>
              {(line, i) => <Line key={i} line={line} wrap={wrap} gutter={gutter} />}
            </VList>
          ) : (
            <div data-slot="patch-lines" className={cn(!wrap && 'w-max min-w-full')}>
              {file.lines.map((line, i) => (
                <Line key={i} line={line} wrap={wrap} gutter={gutter} />
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  )
})

function Line({ line, wrap, gutter }: { line: PatchLine; wrap: boolean; gutter?: string }) {
  return (
    <div className={cn('flex leading-5', !wrap && 'min-w-max', lineTint[line.kind])}>
      {gutter && (
        <>
          <LineNumber value={line.old} width={gutter} />
          <LineNumber value={line.new} width={gutter} last />
        </>
      )}
      <code className={cn('min-w-0 flex-1 px-2', wrap ? 'break-all whitespace-pre-wrap' : 'whitespace-pre')}>
        {prefix[line.kind]}
        {line.text || ' '}
      </code>
    </div>
  )
}

function LineNumber({ value, width, last }: { value?: number; width: string; last?: boolean }) {
  return (
    <span
      aria-hidden
      className={cn('shrink-0 px-2 text-right text-muted tabular-nums select-none', last && 'border-r border-seam')}
      style={{ width }}
    >
      {value ?? ''}
    </span>
  )
}

function lastLine(file: PatchFile): number {
  let last = 0
  for (const line of file.lines) last = Math.max(last, line.old ?? 0, line.new ?? 0)
  return last
}
