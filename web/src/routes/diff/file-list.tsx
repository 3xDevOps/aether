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
        {files.map((file) => {
          const slash = file.path.lastIndexOf('/')
          const { letter, tone, word } = glyph[file.status]
          return (
            <li key={file.path}>
              <ListRow
                selected={file.path === current}
                title={file.path}
                onClick={() => onJump(file.path)}
                leading={
                  <span aria-hidden className={cn('w-3 shrink-0 text-center font-code text-ui-sm', tone)}>
                    {letter}
                  </span>
                }
                trailing={<Counts additions={file.additions} deletions={file.deletions} />}
              >
                {file.path.slice(slash + 1)}
                {slash > 0 && <span className="ml-1.5 text-ui-sm text-muted">{file.path.slice(0, slash)}</span>}
                <span className="sr-only">, {word}</span>
              </ListRow>
            </li>
          )
        })}
      </ul>
    </nav>
  )
}
