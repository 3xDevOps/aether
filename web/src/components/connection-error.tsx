import { CircleAlert, KeyRound, RefreshCw, ServerOff, Unplug, WifiOff } from 'lucide-react'
import type { ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
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
    <main className="flex h-full min-h-0 min-w-0 overflow-y-auto bg-background p-3 sm:p-4">
      <section
        role="alert"
        aria-labelledby="connection-error-title"
        aria-describedby="connection-error-description"
        className="m-auto grid min-w-0 w-full max-w-[720px] overflow-hidden border border-border bg-card"
      >
        <header className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-3 border-b border-border bg-sidebar px-3 py-3 sm:px-4">
          <div className="grid size-7 shrink-0 place-items-center rounded-sm bg-state-needs-attention/15 text-state-needs-attention">
            <Icon className="size-4" aria-hidden />
          </div>
          <div className="min-w-0">
            <p className="mb-0.5 text-xs font-medium text-muted-foreground">{content.eyebrow}</p>
            <h1 id="connection-error-title" className="text-base font-semibold leading-5">
              {content.title}
            </h1>
          </div>
        </header>

        <div className="min-w-0 space-y-3 px-3 py-3 sm:px-4">
          <p id="connection-error-description" className="max-w-[68ch] text-[13px] leading-5 text-muted-foreground">
            {content.description}
          </p>

          {content.action && (
            <div>
              <Button type="button" size="sm" onClick={onRetry}>
                <RefreshCw aria-hidden />
                {content.action}
              </Button>
            </div>
          )}

          {/* Keep the exact raw failure visible and selectable. The bounded
              block owns its scroll so it cannot push retry out of reach. */}
          {error && (
            <Collapsible defaultOpen className="min-w-0 border border-border bg-background text-xs">
              <CollapsibleTrigger className="px-2 text-muted-foreground hover:text-foreground">
                Technical details
              </CollapsibleTrigger>
              <CollapsibleContent className="min-w-0 border-t border-border px-2 py-2">
                <pre className="max-h-[min(14rem,35vh)] overflow-auto whitespace-pre-wrap break-words font-mono leading-5 text-foreground select-text">{error}</pre>
              </CollapsibleContent>
            </Collapsible>
          )}
        </div>
      </section>
    </main>
  )
}
