import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { RefreshCw } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Tone } from '@/components/ui/status-dot'
import { api, ApiError, type Api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import type { AccountAccess, Member, UsageProvider, UsageProviderName, UsageProviderStatus, UsageWindow } from '@/lib/types'
import { cn } from '@/lib/utils'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'

const METHOD_NOT_FOUND = -32601

type UsageClient = Pick<Api, 'accountList' | 'accountUsage'>
type Failure = { kind: 'unauthenticated' | 'unsupported' | 'error'; message: string }

const providerLabels: Record<UsageProviderName, string> = { claude: 'Claude', codex: 'Codex' }
const providerOrder: UsageProviderName[] = ['claude', 'codex']

const statusText: Record<UsageProviderStatus, string> = {
  ok: 'Current',
  stale: 'Stale',
  unauthenticated: 'Not signed in to this provider',
  unsupported: 'Not available on this server',
  unavailable: 'The provider could not report usage right now',
  error: 'The provider returned an error',
}

function failureOf(error: unknown): Failure {
  if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
    return { kind: 'unauthenticated', message: 'Your dashboard session is no longer authorized.' }
  }
  if (error instanceof ApiError && (error.status === 404 || error.code === METHOD_NOT_FOUND)) {
    return { kind: 'unsupported', message: 'This server does not report account usage. Update the server to see it.' }
  }
  if (error instanceof Error && error.message) return { kind: 'error', message: error.message }
  return { kind: 'error', message: 'The usage service did not respond.' }
}

function currentWindows(provider: UsageProvider | undefined, now: number): UsageWindow[] {
  if (!provider || (provider.status !== 'ok' && provider.status !== 'stale')) return []
  return provider.windows.filter((window) => {
    if (!Number.isFinite(window.used_percent) || window.used_percent < 0 || window.used_percent > 100) return false
    if (!window.resets_at) return true
    const reset = Date.parse(window.resets_at)
    return Number.isFinite(reset) && reset > now
  })
}

function resetsIn(resetsAt: string | undefined, now: number): string | null {
  const reset = resetsAt ? Date.parse(resetsAt) : NaN
  if (!Number.isFinite(reset) || reset <= now) return null
  const minutes = Math.max(1, Math.ceil((reset - now) / 60_000))
  if (minutes < 60) return `resets in ${minutes}m`
  const hours = Math.floor(minutes / 60)
  return minutes % 60 === 0 ? `resets in ${hours}h` : `resets in ${hours}h ${minutes % 60}m`
}

function tone(percent: number): Tone {
  if (percent >= 100) return 'failed'
  if (percent >= 80) return 'needs-you'
  return 'done'
}

const fill: Record<Tone, string> = {
  failed: 'bg-state-failed',
  'needs-you': 'bg-state-needs-you',
  done: 'bg-state-done',
  working: 'bg-state-working',
  paused: 'bg-state-paused',
  neutral: 'bg-icon-faint',
}

function ProviderRow({ name, provider, now }: { name: UsageProviderName; provider: UsageProvider | undefined; now: number }) {
  const label = providerLabels[name]
  const status = provider?.status ?? 'unavailable'
  const windows = currentWindows(provider, now)
  const help = status === 'ok' || status === 'stale'
    ? [provider?.plan && `Plan: ${provider.plan}`, status === 'stale' && `Stale: last values from the last successful check${provider?.error ? `: ${provider.error}` : ''}`]
        .filter(Boolean).join('. ') || undefined
    : provider?.error ?? statusText[status]
  return (
    <SettingRow label={label} help={help}>
      {windows.length > 0 ? (
        <ul className="flex min-w-0 flex-col gap-3">
          {windows.map((window) => {
            const reset = resetsIn(window.resets_at, now)
            return (
              <li key={window.id} className="flex min-w-0 flex-col gap-1">
                <div className="flex min-w-0 items-baseline justify-between gap-2 text-ui-sm">
                  <span className="min-w-0 truncate text-text">{window.label}</span>
                  <span className="shrink-0 tabular-nums text-muted">
                    {Math.round(window.used_percent)}% used{reset && ` · ${reset}`}
                  </span>
                </div>
                <span
                  role="progressbar"
                  aria-label={`${label} ${window.label} usage`}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={window.used_percent}
                  className="block h-1.5 overflow-hidden rounded-full bg-chrome"
                >
                  <span className={cn('block h-full', fill[tone(window.used_percent)])} style={{ width: `${window.used_percent}%` }} />
                </span>
              </li>
            )
          })}
        </ul>
      ) : (status === 'ok' || status === 'stale') && <p className="text-ui-sm text-muted">No current measured windows.</p>}
    </SettingRow>
  )
}

function accountsFor(access: AccountAccess, self: Member): Member[] {
  const byID = new Map<string, Member>()
  for (const account of [self, ...access.accounts]) byID.set(account.id, account)
  return [...byID.values()]
}

export function UsageSection({ client = api }: { client?: UsageClient }) {
  const selfID = useStore((s) => s.info?.member?.id ?? null)
  const connection = useStore((s) => s.connection)
  const [selectedID, setSelectedID] = useState<string | null>(selfID)
  const [accounts, setAccounts] = useState<Member[]>(() => {
    const self = useStore.getState().info?.member
    return self ? [self] : []
  })
  const [providers, setProviders] = useState<{ accountID: string; list: UsageProvider[] } | null>(null)
  const [failure, setFailure] = useState<Failure | null>(null)
  const [loading, setLoading] = useState(false)
  const requestID = useRef(0)
  const selected = useRef(selectedID)
  selected.current = selectedID
  useClock()
  const now = Date.now()

  useEffect(() => {
    setSelectedID(selfID)
    setProviders(null)
    setFailure(null)
  }, [selfID])

  useEffect(() => {
    const self = useStore.getState().info?.member
    if (!self || connection !== 'live') return
    let live = true
    client.accountList().then(
      (access) => live && setAccounts(accountsFor(access, self)),
      () => live && setAccounts([self]),
    )
    return () => {
      live = false
    }
  }, [client, connection, selfID])

  const request = useCallback(async (refresh: boolean) => {
    const account = selected.current
    if (!selfID || !account) return
    const id = ++requestID.current
    setLoading(true)
    try {
      const result = await client.accountUsage({
        ...(account === selfID ? {} : { account_member_id: account }),
        ...(refresh ? { refresh: true } : {}),
      })
      if (id !== requestID.current) return
      if (result.account_member_id !== account) {
        setFailure({ kind: 'error', message: 'The usage response belonged to a different account.' })
        setProviders(null)
        return
      }
      setProviders({ accountID: account, list: result.providers })
      setFailure(null)
    } catch (error) {
      if (id !== requestID.current) return
      const next = failureOf(error)
      setFailure(next)
      setProviders((previous) => next.kind !== 'error' || previous?.accountID !== account ? null : {
        accountID: account,
        list: previous.list.map((provider) => provider.status === 'ok' ? { ...provider, status: 'stale' as const, error: next.message } : provider),
      })
    } finally {
      if (id === requestID.current) setLoading(false)
    }
  }, [client, selfID])

  useEffect(() => {
    if (connection !== 'live' || !selectedID) return
    setProviders(null)
    setFailure(null)
    void request(false)
  }, [connection, request, selectedID])

  const byName = useMemo(() => new Map((providers?.list ?? []).map((provider) => [provider.provider, provider])), [providers])
  if (!selfID) return null

  return (
    <SettingsSection title="Usage">
      <SettingRow
        label="Account"
        labelFor="usage-account"
        help="Subscription usage the server reads for Claude and Codex logins. Other agents and API keys are not measured."
        control={(
          <>
            <Select value={selectedID ?? undefined} onValueChange={setSelectedID}>
              <SelectTrigger id="usage-account" className="w-44 max-w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {accounts.map((account) => (
                  <SelectItem key={account.id} value={account.id}>
                    {account.id === selfID ? `${account.display_name} (you)` : account.display_name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button size="icon" variant="ghost" label="Refresh usage" disabled={loading} onClick={() => void request(true)}>
              <RefreshCw />
            </Button>
          </>
        )}
      >
        {failure && (failure.kind !== 'error' || !providers) && (
          <Callout tone={failure.kind === 'unsupported' ? 'neutral' : 'failed'} role={failure.kind === 'error' ? 'alert' : 'status'}>
            {failure.message}
          </Callout>
        )}
        {!providers && !failure && <p role="status" className="text-ui-sm text-muted">{loading ? 'Reading usage…' : 'No usage measured yet.'}</p>}
      </SettingRow>
      {providers && providerOrder.map((name) => <ProviderRow key={name} name={name} provider={byName.get(name)} now={now} />)}
    </SettingsSection>
  )
}
