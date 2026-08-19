import { cn } from '@/lib/utils'
import type { FileStatus, PatchFile } from '@/routes/diff/parse'

const statusLabel: Record<FileStatus, string> = {
  added: 'new',
  deleted: 'deleted',
  modified: '',
  binary: 'binary',
}

const lineClass = {
  add: 'bg-state-done/10',
  del: 'bg-destructive/10',
  hunk: 'bg-muted text-muted-foreground',
  meta: 'text-muted-foreground',
  context: '',
}

/** One file's unified diff. Colour is the whole of the highlighting: the
 * dashboard reads code, it never edits it. */
export function FilePatch({ file }: { file: PatchFile }) {
  return (
    <section className="overflow-hidden rounded-md border">
      <header className="flex items-center gap-2 border-b bg-muted/40 px-2 py-1 text-xs">
        <span className="min-w-0 flex-1 truncate font-medium" title={file.path}>
          {file.path}
        </span>
        {statusLabel[file.status] && (
          <span className="shrink-0 text-muted-foreground">{statusLabel[file.status]}</span>
        )}
        <span className="shrink-0 text-state-done">+{file.additions}</span>
        <span className="shrink-0 text-destructive">-{file.deletions}</span>
      </header>
      <div className="overflow-x-auto">
        <pre className="w-max min-w-full text-xs leading-5">
          {file.lines.map((line, i) => (
            <code
              // Diff lines have no identity of their own; the list is only
              // ever replaced wholesale by the next fetch.
              key={i}
              className={cn('block px-2', lineClass[line.kind])}
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
