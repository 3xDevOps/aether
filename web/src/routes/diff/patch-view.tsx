import { cn } from '@/lib/utils'
import type { FileStatus, PatchFile } from '@/routes/diff/parse'

const statusLabel: Record<FileStatus, string> = {
  added: 'new',
  deleted: 'deleted',
  modified: '',
  binary: 'binary',
}

const lineClass = {
  add: 'bg-state-done/10 text-foreground',
  del: 'bg-destructive/10 text-foreground',
  hunk: 'bg-muted text-muted-foreground',
  meta: 'text-muted-foreground',
  context: '',
}

/** One file's unified diff. Colour is the whole of the highlighting: the
 * dashboard reads code, it never edits it. */
export function FilePatch({ file }: { file: PatchFile }) {
  return (
    <section className="overflow-hidden border-b border-border/80 bg-background">
      <header className="flex min-h-[35px] flex-wrap items-center gap-x-3 gap-y-1 border-b bg-sidebar px-3 py-1 text-[12px]">
        <span className="min-w-0 flex-[1_1_16rem] truncate font-mono font-medium" title={file.path}>
          {file.path}
        </span>
        {statusLabel[file.status] && (
          <span className="shrink-0 text-[11px] uppercase tracking-[0.06em] text-muted-foreground">
            {statusLabel[file.status]}
          </span>
        )}
        <span className="shrink-0 font-mono text-success-foreground">+{file.additions}</span>
        <span className="shrink-0 font-mono text-destructive">-{file.deletions}</span>
      </header>
      <div className="min-w-0 overflow-x-auto overscroll-x-contain">
        <pre className="w-max min-w-full font-mono text-[12px] leading-[22px]">
          {file.lines.map((line, i) => (
            <code
              // Diff lines have no identity of their own; the list is only
              // ever replaced wholesale by the next fetch.
              key={i}
              className={cn('block min-w-max whitespace-pre px-3', lineClass[line.kind])}
            >
              {prefix[line.kind]}
              {line.text || ' '}
            </code>
          ))}
        </pre>
      </div>
    </section>
  )
}

const prefix = { add: '+', del: '-', hunk: '', meta: '', context: ' ' }
