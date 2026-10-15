import { ListRow } from '@/components/ui/list-row'
import { cn } from '@/lib/utils'
import type { FileStatus, PatchFile } from '@/routes/diff/parse'
import { Counts } from '@/routes/diff/patch-view'

const glyph: Record<FileStatus, { letter: string; tone: string; word: string }> = {
  added: { letter: 'A', tone: 'text-diff-add', word: 'added' },
  deleted: { letter: 'D', tone: 'text-diff-del', word: 'deleted' },
  modified: { letter: 'M', tone: 'text-muted', word: 'modified' },
  binary: { letter: 'B', tone: 'text-muted', word: 'binary' },
}

export function ChangedFileRow({
  file,
  selected = false,
  counts = true,
  onOpen,
}: {
  file: { path: string; status?: FileStatus; additions: number; deletions: number }
  selected?: boolean
  counts?: boolean
  onOpen: (path: string) => void
}) {
  const slash = file.path.lastIndexOf('/')
  const status = file.status && glyph[file.status]
  return (
    <ListRow
      selected={selected}
      title={file.path}
      onClick={() => onOpen(file.path)}
      leading={
        <span aria-hidden className={cn('w-3 shrink-0 text-center font-code text-ui-sm', status?.tone)}>
          {status?.letter}
        </span>
      }
      trailing={counts ? <Counts additions={file.additions} deletions={file.deletions} /> : undefined}
    >
      {file.path.slice(slash + 1)}
      {slash > 0 && <span className="ml-1.5 text-ui-sm text-muted">{file.path.slice(0, slash)}</span>}
      {status && <span className="sr-only">, {status.word}</span>}
    </ListRow>
  )
}

export function FileList({
  files,
  current,
  onJump,
}: {
  files: PatchFile[]
  current: string | null
  onJump: (path: string) => void
}) {
  return (
    <nav aria-label="Changed files" className="hidden w-56 shrink-0 overflow-y-auto border-r border-seam bg-chrome p-1 @min-[780px]:block">
      <ul>
        {files.map((file) => (
          <li key={file.path}>
            <ChangedFileRow file={file} selected={file.path === current} onOpen={onJump} />
          </li>
        ))}
      </ul>
    </nav>
  )
}
