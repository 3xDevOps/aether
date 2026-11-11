import { ChevronDown, ChevronUp, GitBranch } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { accessLabel, asFallback, type Readiness, ReadinessText } from '@/components/launch/account-picker'
import { limitsText, type LimitsDraft, parseLimits, RunLimitsFields } from '@/components/launch/run-limits'
import { SubsessionAgents } from '@/components/launch/subsession-agents'
import type { ChildSettings, Member, RunLimits, Workspace } from '@/lib/types'

/** `order` stays null, the server's automatic order, until the reader reorders or unticks an account. */
export type Fallback = { off: boolean; order: string[] | null; excluded: string[] }

export const automaticFallback: Fallback = { off: false, order: null, excluded: [] }

function candidates(fallback: Fallback, accounts: Member[], skip: string): string[] {
  const order = fallback.order ?? []
  return [...order, ...accounts.map((member) => member.id).filter((id) => !order.includes(id))].filter((id) => id !== skip)
}

/** Once the list was changed, an account the order does not hold stays unticked until ticked. */
function ticked(fallback: Fallback, id: string): boolean {
  return !fallback.excluded.includes(id) && (!fallback.order || fallback.order.includes(id))
}

export function fallbackParams(fallback: Fallback, accounts: Member[], skip: string): { fallback_account_member_ids?: string[]; no_fallback?: boolean } {
  if (fallback.off) return { no_fallback: true }
  if (!fallback.order) return {}
  const ids = candidates(fallback, accounts, skip).filter((id) => ticked(fallback, id))
  // An empty list reads as automatic on the server, so unticking every account means off.
  return ids.length ? { fallback_account_member_ids: ids } : { no_fallback: true }
}

export function FallbackOptions({
  accounts,
  ownAccountID,
  account,
  fallback,
  onFallback,
  status,
}: {
  accounts: Member[]
  ownAccountID: string
  account: string
  fallback: Fallback
  onFallback: (fallback: Fallback) => void
  status: (member: Member) => Readiness | null
}) {
  const ids = candidates(fallback, accounts, account)
  const owner = accounts.find((member) => member.id === account)?.display_name ?? account
  const params = fallbackParams(fallback, accounts, account)
  // Editing the shown list keeps the hidden launch account's place and tick
  // (last, ticked, coming from automatic), and leaves every account it did
  // not show ticked unticked.
  const ordered = (shown: string[]) => {
    const all = fallback.order?.includes(account) ? candidates(fallback, accounts, '') : [...shown, account]
    let next = 0
    return {
      order: all.map((id) => (id === account ? id : shown[next++])),
      excluded: [...fallback.excluded, ...all.filter((id) => !fallback.excluded.includes(id) && !ticked(fallback, id))],
    }
  }
  const move = (index: number, by: number) => {
    const order = [...ids]
    ;[order[index], order[index + by]] = [order[index + by], order[index]]
    onFallback({ ...fallback, ...ordered(order) })
  }
  const toggle = (id: string, on: boolean) => {
    const next = ordered(ids)
    onFallback({ ...fallback, order: next.order, excluded: on ? next.excluded.filter((other) => other !== id) : [...next.excluded, id] })
  }
  return (
    <Collapsible>
      <CollapsibleTrigger>
        <span className="shrink-0 font-medium">Fallback</span>
        <span className="shrink-0 text-muted">· {params.no_fallback ? 'Off' : params.fallback_account_member_ids ? 'Ordered' : 'Automatic'} · if</span>
        <span className="min-w-0 truncate text-muted">{owner}</span>
        <span className="shrink-0 text-muted">runs out</span>
      </CollapsibleTrigger>
      <CollapsibleContent className="flex flex-col gap-1 pt-2 pl-5">
        <label className="flex min-h-7 cursor-pointer items-center gap-2 text-ui coarse:min-h-11">
          <Checkbox checked={!fallback.off} onCheckedChange={(on) => onFallback({ ...fallback, off: on !== true })} />
          Fall back to another account
        </label>
        {!fallback.off && (
          <>
            <ul aria-label="Fallback accounts" className="flex flex-col pl-6">
              {ids.map((id, index) => {
                const member = accounts.find((item) => item.id === id)
                const name = member?.display_name ?? id
                return (
                  <li key={id} className="flex min-h-7 min-w-0 items-center gap-1 coarse:min-h-11">
                    <label className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 py-0.5 text-ui">
                      <Checkbox checked={ticked(fallback, id)} onCheckedChange={(on) => toggle(id, on === true)} />
                      <span className="flex min-w-0 flex-col">
                        <span className="flex min-w-0 items-baseline gap-2">
                          <span className="min-w-0 truncate">{name}</span>
                          {member && <span className="shrink-0 text-ui-sm text-muted">{accessLabel(member, ownAccountID)}</span>}
                        </span>
                        {member && <ReadinessText ready={asFallback(status(member))} className="ml-0 max-w-full" />}
                      </span>
                    </label>
                    <Button type="button" variant="ghost" size="icon-sm" label={`Move ${name} up`} disabled={index === 0} onClick={() => move(index, -1)}>
                      <ChevronUp />
                    </Button>
                    <Button type="button" variant="ghost" size="icon-sm" label={`Move ${name} down`} disabled={index === ids.length - 1} onClick={() => move(index, 1)}>
                      <ChevronDown />
                    </Button>
                  </li>
                )
              })}
            </ul>
            <p className="text-ui-sm text-muted">
              If {owner}&apos;s usage is 95% or more at launch, or the vendor reports the limit mid-session, the session moves to {fallback.order ? 'the first ticked account, top to bottom, with usage left' : 'the ticked account with the most usage left'}.
            </p>
          </>
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}

export type ChildRunsChoice = '' | 'on' | 'off'

/** Whether this run may start child runs; it follows the member setting until flipped, and only a flip is sent. */
export function ChildRunsSwitch({ value, onChange, enabled, settings, readError, unavailable, disabled }: {
  value: ChildRunsChoice
  onChange: (value: ChildRunsChoice) => void
  /** The member setting, unknown until read. */
  enabled: boolean | undefined
  /** The member's whole setting, whose agents the switch offers while on. */
  settings?: ChildSettings | null
  /** Why the setting could not be read; the switch then sets this run alone. */
  readError?: string | null
  /** Why this run cannot start child runs whatever the setting says. */
  unavailable?: string
  disabled?: boolean
}) {
  const on = !unavailable && (value ? value === 'on' : enabled === true)
  return (
    <div className="flex flex-col gap-1">
      <div className="flex min-h-7 items-center justify-between gap-3 coarse:min-h-11">
        <Label htmlFor="launch-child-runs">Subsessions</Label>
        <Switch
          id="launch-child-runs"
          aria-describedby="launch-child-runs-help"
          checked={on}
          disabled={disabled || !!unavailable || (enabled === undefined && !readError)}
          onCheckedChange={(next) => onChange(enabled !== undefined && next === enabled ? '' : next ? 'on' : 'off')}
        />
      </div>
      <p id="launch-child-runs-help" className="text-ui-sm text-muted">
        {unavailable ?? (enabled === undefined ? (readError ? `Could not read your setting: ${readError}` : 'Reading your subsessions setting…') : ['Lets the agent start other agents as subsessions.', value ? 'Set for this session only.' : `Follows your setting, which is ${enabled ? 'on' : 'off'}.`].join(' ').trim())}
      </p>
      {on && settings && (settings.choices?.some((c) => c.status !== 'not-installed') ?? false) && (
        <div className="flex min-h-7 items-center justify-between gap-3 coarse:min-h-11">
          <Label htmlFor="launch-child-agents" className="text-muted">Subsession agents</Label>
          <SubsessionAgents id="launch-child-agents" settings={settings} disabled={disabled} />
        </div>
      )}
    </div>
  )
}

export function WorkspaceLine({ workspace }: { workspace: Workspace | undefined }) {
  return (
    <p aria-label="Target workspace" className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
      {workspace ? (
        <>
          <GitBranch aria-hidden className="size-3.5 shrink-0" />
          <span className="min-w-0 truncate">
            <span className="text-text">{workspace.name}</span> from <code className="font-code">{workspace.base_branch}</code>
          </span>
        </>
      ) : (
        'Pick a workspace in the sidebar first.'
      )}
    </p>
  )
}

/** The run's own limits, collapsed until opened; an empty field follows the server's limit. */
export function RunLimitsOption({ value, onChange, effective, disabled }: {
  value: LimitsDraft
  onChange: (value: LimitsDraft) => void
  /** What a run with no override gets, unknown until read. */
  effective: RunLimits | undefined
  disabled?: boolean
}) {
  const parsed = parseLimits(value)
  const summary = parsed.limits ? limitsText(parsed.limits) : ''
  return (
    <Collapsible defaultOpen={summary !== ''}>
      <CollapsibleTrigger>
        <span className="font-medium">Resource limits</span>
        {summary && <span className="min-w-0 truncate text-muted">· {summary}</span>}
      </CollapsibleTrigger>
      <CollapsibleContent className="flex flex-col gap-2 pt-2 pl-5">
        <RunLimitsFields idPrefix="launch-limit" value={value} onChange={onChange} placeholders={effective} disabled={disabled} />
        <p className={parsed.error ? 'text-ui-sm text-state-failed' : 'text-ui-sm text-muted'} role={parsed.error ? 'alert' : undefined}>
          {parsed.error ?? 'Caps this run only. An empty field uses the server limit, shown in grey.'}
        </p>
      </CollapsibleContent>
    </Collapsible>
  )
}
