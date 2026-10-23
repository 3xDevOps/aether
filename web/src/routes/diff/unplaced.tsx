import { CommentCard } from '@/routes/diff/comment-card'
import type { PatchFile } from '@/routes/diff/parse'
import { useReview } from '@/routes/diff/review'

/** Comments on a file this diff no longer has. They stay, with their quote, until sent or deleted. */
export function Unplaced({ runID, files, scope }: { runID: string; files: PatchFile[]; scope: string }) {
  const { comments } = useReview(runID)
  const gone = comments.filter((comment) => comment.scope === scope && !files.some((file) => file.path === comment.path))
  if (gone.length === 0) return null
  return (
    <section aria-label="Outdated comments" className="flex flex-col gap-2 border-b border-seam bg-chrome p-2">
      {gone.map((comment) => (
        <CommentCard key={comment.id} runID={runID} comment={comment} lines={comment.lines} outdated />
      ))}
    </section>
  )
}
