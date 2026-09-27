// The edge gateway's two answers before it serves a browser anything: sign
// in, or wait for this browser to be approved as a device. Both replace the
// shell, as a connection error does, because nothing behind them loads.

import { Hourglass, LogIn, LogOut, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { toast } from 'sonner'
import { StatusPage } from '@/components/connection-error'
import { CopyableCommand } from '@/components/copyable-command'
import { Button, buttonVariants } from '@/components/ui/button'
import { api, loginPath, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import type { EdgeAccess } from '@/store/server'

/**
 * Ends the session and reloads, so nothing the signed-out browser held stays
 * in memory; the reload lands on the sign-in page.
 */
async function signOut(client: Api): Promise<void> {
  await client.signOut()
  window.location.replace('/')
}

export function EdgeAccessPage({
  access,
  onRetry,
  client = api,
}: {
  access: EdgeAccess
  onRetry: () => void
  client?: Api
}) {
  if (access.state === 'signed-out') {
    return (
      <StatusPage
        icon={LogIn}
        eyebrow="Sign in"
        title="Sign in to this Aether server"
        description="This dashboard is served by your Aether server through an edge relay. Sign in with the GitHub or Google account the server knows you by."
      >
        <div>
          <a href={loginPath} className={buttonVariants({ size: 'sm' })}>
            <LogIn aria-hidden />
            Sign in
          </a>
        </div>
      </StatusPage>
    )
  }
  return <PendingDevice code={access.approvalCode} onRetry={onRetry} client={client} />
}

function PendingDevice({
  code,
  onRetry,
  client,
}: {
  code: string
  onRetry: () => void
  client: Api
}) {
  const [error, setError] = useState<string | null>(null)
  return (
    <StatusPage
      icon={Hourglass}
      eyebrow="Waiting for approval"
      title="This browser is waiting for approval"
      description="You are signed in. The server holds each member's second and later devices, browsers included, until a device they already use or an admin approves them."
    >
      <div className="space-y-1">
        <p className="text-xs text-muted-foreground">Approval code</p>
        <p className="font-mono text-base font-semibold tracking-wider select-all">{code}</p>
      </div>
      <div className="min-w-0 space-y-1">
        <p className="text-[13px] leading-5">From a computer you already use, or as an admin:</p>
        <CopyableCommand command={`aether device approve ${code}`} />
      </div>
      <div className="min-w-0 space-y-1">
        <p className="text-[13px] leading-5">On the server host:</p>
        <CopyableCommand command={`sudo aether-server device approve ${code}`} />
      </div>
      <p className="text-[13px] leading-5 text-muted-foreground">
        A dashboard that is already signed in approves the code under Devices.
      </p>
      {error && (
        <p role="alert" className="border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed">
          {error}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" size="sm" onClick={onRetry}>
          <RefreshCw aria-hidden />
          Check again
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => {
            setError(null)
            signOut(client).catch((err: unknown) => setError(message(err)))
          }}
        >
          <LogOut aria-hidden />
          Sign out
        </Button>
      </div>
    </StatusPage>
  )
}

/** The shell's way out of an edge session; nothing on another gateway. */
export function SignOutButton({ client = api }: { client?: Api }) {
  const gateway = useStore((s) => s.capabilities?.gateway)
  if (gateway !== 'edge') return null
  return (
    <button
      type="button"
      onClick={() => {
        signOut(client).catch((err: unknown) => toast.error(message(err)))
      }}
      className={cn(
        focusRing,
        'flex h-[var(--status-bar-height)] min-h-[var(--status-bar-height)] shrink-0 items-center gap-1 rounded-sm px-1 hover:text-foreground',
      )}
    >
      <LogOut className="size-3.5" aria-hidden />
      Sign out
    </button>
  )
}
