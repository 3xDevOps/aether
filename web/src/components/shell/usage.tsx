import { GaugeIcon, RefreshCwIcon } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api, ApiError, type Api } from '@/lib/api'
import type {
  AccountAccess,
  Member,
  UsageProvider,
  UsageProviderName,
  UsageProviderStatus,
  UsageWindow,
} from '@/lib/types'
import type { ConnectionState } from '@/lib/stream'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'

const USAGE_POLL_MS = 60_000
const METHOD_NOT_FOUND = -32601

type UsageClient = Pick<Api, 'accountList' | 'accountUsage'>
type FailureKind = 'unauthenticated' | 'unsupported' | 'error'
type Failure = { kind: FailureKind; message?: string }

type ReaderState = {
  accountID: string
  providers: UsageProvider[]
}

const providerLabels: Record<UsageProviderName, string> = {
  claude: 'Claude',
  codex: 'Codex',
}

const providerOrder: UsageProviderName[] = ['claude', 'codex']

function isUnauthorized(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 401 || error.status === 403)
}

function isMethodMissing(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 404 || error.code === METHOD_NOT_FOUND)
}

function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message
  if (error instanceof Error && error.message) return error.message
  return 'The usage service did not respond.'
}

function validWindow(window: UsageWindow, now: number): boolean {
  if (!Number.isFinite(window.used_percent) || window.used_percent < 0 || window.used_percent > 100) {
    return false
  }
  if (!window.resets_at) return true
  const reset = Date.parse(window.resets_at)
  return Number.isFinite(reset) && reset > now
}

function currentWindows(provider: UsageProvider | null, now: number): UsageWindow[] {
  if (!provider || (provider.status !== 'ok' && provider.status !== 'stale')) return []
  return provider.windows.filter((window) => validWindow(window, now))
}

function worstLeft(provider: UsageProvider | null, now: number): number | null {
  const windows = currentWindows(provider, now)
  if (windows.length === 0) return null
  return Math.min(...windows.map((window) => 100 - window.used_percent))
}

function formatCountdown(resetsAt: string | undefined, now: number): string | null {
  if (!resetsAt) return null
  const reset = Date.parse(resetsAt)
  if (!Number.isFinite(reset) || reset <= now) return null
  const minutes = Math.max(1, Math.ceil((reset - now) / 60_000))
  if (minutes < 60) return `resets in ${minutes}m`
  const hours = Math.floor(minutes / 60)
  const remaining = minutes % 60
  return remaining === 0 ? `resets in ${hours}h` : `resets in ${hours}h ${remaining}m`
}

function severity(percent: number): 'ok' | 'warning' | 'exhausted' {
  if (percent >= 100) return 'exhausted'
  if (percent >= 80) return 'warning'
  return 'ok'
}

function severityClass(level: ReturnType<typeof severity>): string {
  if (level === 'exhausted') return 'text-state-failed'
  if (level === 'warning') return 'text-state-needs-attention'
  return 'text-state-done'
}

function statusLabel(status: UsageProviderStatus): string {
  switch (status) {
    case 'ok':
      return 'Current'
    case 'stale':
      return 'Stale'
    case 'unauthenticated':
      return 'Not authenticated'
    case 'unsupported':
      return 'Unsupported'
    case 'unavailable':
      return 'Unavailable'
    case 'error':
      return 'Error'
  }
}

function ProviderUsage({ provider, now }: { provider: UsageProvider | null; now: number }) {
  const name = provider?.provider ?? 'claude'
  const label = providerLabels[name]
  const windows = currentWindows(provider, now)
  const left = worstLeft(provider, now)
  const status = provider?.status ?? 'unavailable'
  const update = provider?.updated_at
  const error = provider?.error
  const stale = status === 'stale'

  return (
    <section className="min-w-0 border-t border-border pt-2 first:border-t-0 first:pt-0" aria-labelledby={`usage-${name}`}>
      <div className="flex min-w-0 items-baseline justify-between gap-2">
        <h3 id={`usage-${name}`} className="min-w-0 truncate font-medium">
          {label}
        </h3>
        <div className="flex shrink-0 items-baseline gap-2 text-[12px]">
          <span className={cn(status === 'ok' ? 'text-state-done' : 'text-muted-foreground')}>
            {statusLabel(status)}
          </span>
          {left !== null && (
            <span className={cn('font-medium', severityClass(severity(100 - left)))}>
              {Math.round(left)}% left
            </span>
          )}
        </div>
      </div>

      {stale && (
        <p className="mt-1 text-[12px] text-state-needs-attention">
          Stale values are from the last successful check{error ? `: ${error}` : '.'}
        </p>
      )}
      {(status === 'unauthenticated' || status === 'unsupported' || status === 'unavailable' || status === 'error') && (
        <p className={cn('mt-1 break-words text-[12px]', status === 'error' ? 'text-state-failed' : 'text-muted-foreground')}>
          {error ??
            (status === 'unauthenticated'
              ? 'Sign in to this provider to read subscription usage.'
              : status === 'unsupported'
                ? 'This provider is not available on this server.'
                : status === 'unavailable'
                  ? 'The provider could not report usage right now.'
                  : 'The provider returned an error.')}
        </p>
      )}

      {windows.length > 0 ? (
        <div className="mt-2 space-y-2">
          {windows.map((window) => {
            const level = severity(window.used_percent)
            const countdown = formatCountdown(window.resets_at, now)
            return (
              <div key={window.id} className="min-w-0">
                <div className="flex min-w-0 items-baseline justify-between gap-2 text-[12px]">
                  <span className="min-w-0 truncate">{window.label}</span>
                  <span className={cn('shrink-0', severityClass(level))}>
                    {level === 'exhausted' ? 'Exhausted' : level === 'warning' ? 'Warning' : `${Math.round(window.used_percent)}% used`}
                  </span>
                </div>
                <div
                  className="mt-1 h-1.5 overflow-hidden rounded-sm bg-muted"
                  role="progressbar"
                  aria-label={`${label} ${window.label} usage`}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={window.used_percent}
                >
                  <span
                    className={cn(
                      'block h-full',
                      level === 'exhausted'
                        ? 'bg-state-failed'
                        : level === 'warning'
                          ? 'bg-state-needs-attention'
                          : 'bg-state-done',
                    )}
                    style={{ width: `${window.used_percent}%` }}
                  />
                </div>
                <div className="mt-0.5 flex min-w-0 flex-wrap gap-x-2 text-[11px] leading-4 text-muted-foreground">
                  <span>{Math.round(window.used_percent)}% used · {Math.round(100 - window.used_percent)}% left</span>
                  {countdown && <span>{countdown}</span>}
                  {window.resets_at && (
                    <time dateTime={window.resets_at} title={window.resets_at}>
                      {window.resets_at}
                    </time>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      ) : (
        <p className="mt-2 text-[12px] text-muted-foreground">
          {status === 'ok' || stale ? 'No current measured windows.' : 'No measured windows.'}
        </p>
      )}

      {provider?.plan && <p className="mt-2 text-[12px] text-muted-foreground">Plan: {provider.plan}</p>}
      {update && (
        <p className="mt-1 text-[11px] leading-4 text-muted-foreground">
          Last successful update: <time dateTime={update}>{update}</time>
        </p>
      )}
      {provider && (
        <p className="mt-1 text-[11px] leading-4 text-muted-foreground">
          Checked: <time dateTime={provider.checked_at}>{provider.checked_at}</time>
        </p>
      )}
    </section>
  )
}

function accountsFor(access: AccountAccess, self: Member): Member[] {
  const byID = new Map<string, Member>()
  for (const account of [self, ...access.accounts]) byID.set(account.id, account)
  return [...byID.values()]
}

function accountLabel(account: Member, selfID: string): string {
  return account.id === selfID ? `${account.display_name} (you)` : account.display_name
}

export function UsageReader({ client = api }: { client?: UsageClient }) {
  const member = useStore((s) => s.info?.member)
  const connection = useStore((s) => s.connection)
  const selfID = member?.id ?? null
  const [selectedID, setSelectedID] = useState<string | null>(selfID)
  const [accounts, setAccounts] = useState<Member[]>([])
  const [accountsLoading, setAccountsLoading] = useState(false)
  const [accountsLoaded, setAccountsLoaded] = useState(false)
  const [reader, setReader] = useState<ReaderState | null>(null)
  const [failure, setFailure] = useState<Failure | null>(null)
  const [loading, setLoading] = useState(false)
  const [open, setOpen] = useState(false)
  const [now, setNow] = useState(() => Date.now())
  const requestID = useRef(0)
  const identity = useRef({ selfID, selectedID })
  const previousConnection = useRef<ConnectionState | undefined>(undefined)
  const automaticBlocked = failure?.kind === 'unsupported' || failure?.kind === 'unauthenticated'
  const automaticBlockedRef = useRef(automaticBlocked)

  useEffect(() => {
    identity.current = { selfID, selectedID }
  }, [selfID, selectedID])

  useEffect(() => {
    automaticBlockedRef.current = automaticBlocked
  }, [automaticBlocked])

  useEffect(() => {
    setReader(null)
    setFailure(null)
    requestID.current += 1
  }, [selectedID])

  useEffect(() => {
    setSelectedID(selfID)
    setAccounts(member ? [member] : [])
    setAccountsLoaded(false)
    setReader(null)
    setFailure(null)
    setLoading(false)
    requestID.current += 1
  }, [selfID])

  const request = useCallback(
    async (refresh: boolean) => {
      const { selfID: requestSelf, selectedID: requestAccount } = identity.current
      if (!requestSelf || !requestAccount) return
      const id = ++requestID.current
      setLoading(true)
      try {
        const result = await client.accountUsage({
          ...(requestAccount === requestSelf ? {} : { account_member_id: requestAccount }),
          ...(refresh ? { refresh: true } : {}),
        })
        const current = identity.current
        if (id !== requestID.current || current.selfID !== requestSelf || current.selectedID !== requestAccount) return
        if (result.account_member_id !== requestAccount) {
          setFailure({ kind: 'error', message: 'The usage response belonged to a different account.' })
          setReader(null)
          return
        }
        setReader({ accountID: requestAccount, providers: result.providers })
        setFailure(null)
        setNow(Date.now())
      } catch (error) {
        const current = identity.current
        if (id !== requestID.current || current.selfID !== requestSelf || current.selectedID !== requestAccount) return
        if (isUnauthorized(error)) {
          setReader(null)
          setFailure({ kind: 'unauthenticated', message: 'Your dashboard session is no longer authorized.' })
        } else if (isMethodMissing(error)) {
          setReader(null)
          setFailure({ kind: 'unsupported', message: 'This server does not provide account usage. Update the server to enable subscription monitoring.' })
        } else {
          setFailure({ kind: 'error', message: errorMessage(error) })
          setReader((previous) => {
            if (!previous || previous.accountID !== requestAccount) return previous
            return {
              ...previous,
              providers: previous.providers.map((provider) =>
                provider.status === 'ok'
                  ? { ...provider, status: 'stale' as const, error: errorMessage(error) }
                  : provider,
              ),
            }
          })
        }
      } finally {
        if (id === requestID.current) setLoading(false)
      }
    },
    [client],
  )

  useEffect(() => {
    if (!selfID || !selectedID || connection !== 'live') return
    const wasConnected = previousConnection.current === 'live'
    previousConnection.current = connection
    void request(wasConnected)
    const interval = window.setInterval(() => {
      if (document.visibilityState === 'visible' && !automaticBlockedRef.current) {
        void request(false)
      }
    }, USAGE_POLL_MS)
    const onFocus = () => {
      if (document.visibilityState === 'visible' && !automaticBlockedRef.current) {
        void request(true)
      }
    }
    const onVisibility = () => {
      if (document.visibilityState === 'visible' && !automaticBlockedRef.current) {
        void request(true)
      }
    }
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      window.clearInterval(interval)
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [connection, request, selectedID, selfID])

  useEffect(() => {
    if (!open || !member || accountsLoaded || connection !== 'live') return
    let live = true
    setAccountsLoading(true)
    void client.accountList()
      .then((access) => {
        if (!live || identity.current.selfID !== member.id) return
        setAccounts(accountsFor(access, member))
        setAccountsLoaded(true)
      })
      .catch(() => {
        if (live) setAccounts(member ? [member] : [])
      })
      .finally(() => {
        if (live) setAccountsLoading(false)
      })
    return () => {
      live = false
    }
  }, [accountsLoaded, client, connection, member, open])

  useEffect(() => {
    if (!reader) return
    const syncNow = () => {
      if (document.visibilityState === 'visible') setNow(Date.now())
    }
    syncNow()
    const interval = window.setInterval(syncNow, 30_000)
    window.addEventListener('focus', syncNow)
    document.addEventListener('visibilitychange', syncNow)
    return () => {
      window.clearInterval(interval)
      window.removeEventListener('focus', syncNow)
      document.removeEventListener('visibilitychange', syncNow)
    }
  }, [reader])

  const providerMap = useMemo(() => {
    const map = new Map<UsageProviderName, UsageProvider>()
    for (const provider of reader?.providers ?? []) map.set(provider.provider, provider)
    return map
  }, [reader])
  const summaryLeft = useMemo(() => {
    const values = providerOrder
      .map((provider) => worstLeft(providerMap.get(provider) ?? null, now))
      .filter((value): value is number => value !== null)
    return values.length > 0 ? Math.min(...values) : null
  }, [now, providerMap])
  const refresh = () => {
    setFailure(null)
    void request(true)
  }

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (next && reader) setNow(Date.now())
      }}
    >
      <PopoverTrigger asChild>
        <button
          type="button"
          className={cn(
            focusRing,
            'flex h-[var(--status-bar-height)] min-h-[var(--status-bar-height)] min-w-0 shrink-0 items-center gap-1 rounded-sm px-1 text-muted-foreground hover:bg-toolbar-hover hover:text-foreground',
          )}
          aria-label="Usage"
        >
          <GaugeIcon className="size-3.5 shrink-0" aria-hidden />
          <span className="truncate">Usage</span>
          {summaryLeft !== null && <span className="hidden xl:inline">{Math.round(summaryLeft)}% left</span>}
          {loading && <span className="sr-only">Loading</span>}
        </button>
      </PopoverTrigger>
      <PopoverContent side="top" align="start" aria-labelledby="usage-popover-title">
        <div className="flex min-w-0 items-start justify-between gap-3">
          <div className="min-w-0">
            <h2 id="usage-popover-title" className="font-medium">Subscription usage</h2>
            <p className="text-[12px] leading-4 text-muted-foreground">
              Read-only measurements for the selected account.
            </p>
          </div>
          <button
            type="button"
            onClick={refresh}
            className={cn(focusRing, 'flex size-7 shrink-0 items-center justify-center rounded-sm hover:bg-toolbar-hover')}
            aria-label="Refresh usage"
            disabled={loading}
          >
            <RefreshCwIcon className={cn('size-3.5', loading && 'animate-spin motion-reduce:animate-none')} aria-hidden />
          </button>
        </div>

        <div className="mt-3 space-y-1">
          <label className="block text-[12px] text-muted-foreground" htmlFor="usage-account">Account</label>
          <Select value={selectedID ?? undefined} onValueChange={setSelectedID}>
            <SelectTrigger id="usage-account" aria-label="Account">
              <SelectValue placeholder={accountsLoading ? 'Loading accounts…' : 'Choose an account'} />
            </SelectTrigger>
            <SelectContent>
              {accounts.map((account) => (
                <SelectItem key={account.id} value={account.id}>
                  {accountLabel(account, selfID ?? '')}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="mt-3 space-y-3">
          {failure?.kind === 'unsupported' && (
            <p role="status" className="border border-border bg-muted px-2 py-1.5 text-[12px] text-muted-foreground">
              {failure.message}
            </p>
          )}
          {failure?.kind === 'unauthenticated' && (
            <p role="status" className="border border-border bg-muted px-2 py-1.5 text-[12px] text-state-needs-attention">
              {failure.message}
            </p>
          )}
          {failure?.kind === 'error' && !reader && (
            <p role="alert" className="break-words border border-border bg-muted px-2 py-1.5 text-[12px] text-state-failed">
              {failure.message}
            </p>
          )}
          {!reader && !failure && (
            <p role="status" className="text-[12px] text-muted-foreground">{loading ? 'Loading usage…' : 'No usage has been measured yet.'}</p>
          )}
          {reader && providerOrder.map((provider) => (
            <ProviderUsage key={provider} provider={providerMap.get(provider) ?? null} now={now} />
          ))}
          <p className="border-t border-border pt-2 text-[11px] leading-4 text-muted-foreground">
            Pi, OpenCode, OMP/custom harnesses, API-key usage and billing history are not supported here. Tokens stay on the server.
          </p>
          {automaticBlocked && <span className="sr-only">Automatic refresh is paused until the next manual refresh or reconnect.</span>}
        </div>
      </PopoverContent>
    </Popover>
  )
}
