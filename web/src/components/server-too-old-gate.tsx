import { useEffect, useRef } from 'react'
import { CircleAlert } from '@/components/icons'
import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'

/**
 * This client is newer than its server. Unlike the update gate nothing is
 * refused, so the member may carry on; the server is updated by an admin.
 */
export function ServerTooOldGate({
  client,
  server,
  onContinue,
}: {
  client: string
  server: string
  onContinue: () => void
}) {
  const headingRef = useRef<HTMLHeadingElement>(null)
  useEffect(() => headingRef.current?.focus(), [])
  return (
    <main className="flex h-full min-h-0 min-w-0 overflow-y-auto bg-canvas p-3 sm:p-4">
      <section
        aria-labelledby="server-too-old-title"
        className="m-auto grid min-w-0 w-full max-w-[720px] grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 border border-seam bg-canvas p-3 sm:p-4"
      >
        <p className="col-start-2 row-start-1 text-ui-sm font-medium text-muted">
          {client} app · {server} server
        </p>
        <CircleAlert className="row-start-2 size-4 shrink-0 self-center text-state-needs-you" aria-hidden />
        <h1 id="server-too-old-title" ref={headingRef} tabIndex={-1} className="col-start-2 row-start-2 text-title leading-5 outline-none">
          This server is older than this app
        </h1>
        <div className="col-start-2 row-start-3 min-w-0 space-y-3 pt-2">
          <p className="max-w-[68ch] text-ui leading-5 text-muted">
            Newer screens may fail against it. A server admin updates it with:
          </p>
          <CopyableCommand command="aether server update" />
          <Button size="sm" variant="secondary" onClick={onContinue}>
            Continue anyway
          </Button>
        </div>
      </section>
    </main>
  )
}
