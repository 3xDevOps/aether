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
        className="m-auto grid min-w-0 w-full max-w-[720px] gap-3 border border-seam bg-canvas p-3 sm:p-4"
      >
        <div className="flex items-start gap-3">
          <CircleAlert className="mt-0.5 size-4 shrink-0 text-state-needs-you" aria-hidden />
          <div className="min-w-0 space-y-1">
            <p className="text-ui-sm font-medium text-muted">Server too old</p>
            <h1 id="server-too-old-title" ref={headingRef} tabIndex={-1} className="text-title leading-5 outline-none">
              This server is older than this app
            </h1>
          </div>
        </div>
        <p className="max-w-[68ch] text-ui leading-5 text-muted">
          This app is {client} and the server runs {server}. Newer screens may fail against it. A server admin
          updates the server first:
        </p>
        <CopyableCommand command="aether server update" />
        <div>
          <Button size="sm" variant="secondary" onClick={onContinue}>
            Continue anyway
          </Button>
        </div>
      </section>
    </main>
  )
}
