import { CircleAlert, KeyRound, RefreshCw, ServerOff, Unplug, WifiOff } from 'lucide-react'
import type { ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { cn, focusRing } from '@/lib/utils'
import type { UnreachableKind } from '@/store/server'
type ConnectionErrorProps = {
  kind: UnreachableKind | null
  dead: boolean
  error: string | null
  onRetry: () => void
}

type ErrorCopy = {
  icon: typeof ServerOff
  eyebrow: string
  title: string
  description: ReactNode
  action: string | null
}

/** A command the user is told to run, styled so it reads as one. */
function Cmd({ children }: { children: string }) {
  return (
    <code className="inline-flex rounded-sm border border-border/70 bg-muted px-1.5 py-0.5 font-mono text-xs text-foreground">
      {children}
    </code>
  )
}

/**
 * One failure, one instruction. Which hop died decides what the user can
 * actually do about it, so each case names that hop and stops: a dead local
 * network is not the server's fault, and telling someone to check a server
 * they never reached sends them to fix the wrong thing.
 */
function copyFor({ kind, dead }: ConnectionErrorProps): ErrorCopy {
  if (dead) {
    return {
      icon: KeyRound,
      eyebrow: 'Dashboard link expired',
      title: 'This dashboard link has expired',
      description: (
        <>
          The dashboard token is no longer valid. Open a new link with{' '}
          <Cmd>aether gui</Cmd> and try again.
        </>
      ),
      action: null,
    }
  }

  if (kind === 'network') {
    return {
      icon: WifiOff,
      eyebrow: 'No connection',
      title: 'This computer is offline',
      description:
        'Your machine could not reach the network at all, so nothing was asked of the server yet. Reconnect to wifi or your VPN, then try again.',
      action: 'Retry connection',
    }
  }

  if (kind === 'server') {
    return {
      icon: ServerOff,
      eyebrow: 'Server offline',
      title: 'Cannot reach your Aether server',
      description:
        'The network is up and the server did not answer over SSH. Check that the server is running and that its host is reachable, then try again.',
      action: 'Retry connection',
    }
  }

  if (kind === 'gateway') {
    return {
      icon: Unplug,
      eyebrow: 'Local gateway offline',
      title: 'Cannot reach the dashboard gateway',
      description: (
        <>
          The local dashboard gateway stopped answering. Restart the desktop app, or run{' '}
          <Cmd>aether gui</Cmd> again, then retry.
        </>
      ),
      action: 'Retry connection',
    }
  }

  return {
    icon: CircleAlert,
    eyebrow: 'Connection problem',
    title: 'The dashboard could not load',
    description:
      'The dashboard did not get a usable answer. Check your connection and try again.',
    action: 'Retry connection',
  }
}

/**
 * The whole window when the app has no data to show. It replaces the shell
 * rather than sitting inside it: an empty sidebar and an empty board around
 * a toast tell the user nothing about what broke or what to do next.
 */
export function ConnectionError({ kind, dead, error, onRetry }: ConnectionErrorProps) {
  const content = copyFor({ kind, dead, error, onRetry })
  const Icon = content.icon

  return (
    <main className="flex h-full min-h-[24rem] items-center justify-center bg-background px-4 py-8 sm:px-6">
      <section
        role="alert"
        aria-labelledby="connection-error-title"
        aria-describedby="connection-error-description"
        className="w-full max-w-2xl overflow-hidden rounded-lg border border-border/80 bg-card shadow-sm"
      >
        <header className="flex items-start gap-4 border-b border-border/70 px-5 py-5 sm:px-7 sm:py-6">
          <div className="grid size-10 shrink-0 place-items-center rounded-md bg-state-needs-attention/15 text-state-needs-attention">
            <Icon className="size-5" aria-hidden />
          </div>
          <div className="min-w-0">
            <p className="mb-1 text-xs font-medium uppercase tracking-[0.14em] text-muted-foreground">
              {content.eyebrow}
            </p>
            <h1 id="connection-error-title" className="text-xl font-semibold tracking-tight sm:text-2xl">
              {content.title}
            </h1>
          </div>
        </header>

        <div className="space-y-5 px-5 py-5 sm:px-7 sm:py-6">
          <p
            id="connection-error-description"
            className="max-w-xl text-sm leading-6 text-muted-foreground"
          >
            {content.description}
          </p>

          {content.action && (
            <div>
              <Button type="button" className="h-9" onClick={onRetry}>
                <RefreshCw aria-hidden />
                {content.action}
              </Button>
            </div>
          )}

          {/* The raw failure remains open so a fatal screen never hides the
              exact message someone needs to relay or act on. It can still be
              collapsed once it has been read. */}
          {error && (
            <details open className="rounded-md border border-border/70 bg-muted/30 p-3 text-xs">
              <summary
                className={cn(
                  focusRing,
                  'cursor-pointer select-none font-medium text-muted-foreground hover:text-foreground',
                )}
              >
                Technical details
              </summary>
              <p className="mt-2 break-words font-mono leading-5 text-foreground">{error}</p>
            </details>
          )}
        </div>
      </section>
    </main>
  )
}
