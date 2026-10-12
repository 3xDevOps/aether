import { ChevronDown, ChevronUp, GitBranch } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Member, Workspace } from '@/lib/types'

/** `order` stays null, the server's automatic order, until the reader reorders or unticks an account. */
export type Fallback = { off: boolean; order: string[] | null; excluded: string[] }

export const automaticFallback: Fallback = { off: false, order: null, excluded: [] }

function candidates(fallback: Fallback, accounts: Member[], skip: string): string[] {
  const order = fallback.order ?? []
  return [...order, ...accounts.map((member) => member.id).filter((id) => !order.includes(id))].filter((id) => id !== skip)
}

export function fallbackParams(fallback: Fallback, accounts: Member[], skip: string): { fallback_account_member_ids?: string[]; no_fallback?: boolean } {
  if (fallback.off) return { no_fallback: true }
  if (!fallback.order) return {}
  const ids = candidates(fallback, accounts, skip).filter((id) => !fallback.excluded.includes(id))
  // An empty list reads as automatic on the server, so unticking every account means off.
  return ids.length ? { fallback_account_member_ids: ids } : { no_fallback: true }
}

function FallbackList({
  accounts,
  account,
  skip,
  accountName,
  fallback,
  onChange,
}: {
  accounts: Member[]
  account: string
  skip: string
  accountName: (member: Member) => string
  fallback: Fallback
  onChange: (fallback: Fallback) => void
}) {
  const ids = candidates(fallback, accounts, skip)
  const owner = accounts.find((member) => member.id === account)?.display_name ?? account
  const move = (index: number, by: number) => {
    const order = [...ids]
    ;[order[index], order[index + by]] = [order[index + by], order[index]]
    onChange({ ...fallback, order })
  }
  const toggle = (id: string, on: boolean) => onChange({
    ...fallback,
    order: fallback.order ?? ids,
    excluded: on ? fallback.excluded.filter((other) => other !== id) : [...fallback.excluded, id],
  })
  return (
    <div className="flex flex-col gap-1 pt-2">
      <label className="flex min-h-7 cursor-pointer items-center gap-2 text-ui coarse:min-h-11">
        <Checkbox checked={!fallback.off} onCheckedChange={(on) => onChange({ ...fallback, off: on !== true })} />
        Fall back to another account
      </label>
      {!fallback.off && (
        <>
          <ul aria-label="Fallback accounts" className="flex flex-col pl-6">
            {ids.map((id, index) => {
              const member = accounts.find((item) => item.id === id)
              const name = member ? accountName(member) : id
              return (
                <li key={id} className="flex min-h-7 items-center gap-2 coarse:min-h-11">
                  <label className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-ui">
                    <Checkbox checked={!fallback.excluded.includes(id)} onCheckedChange={(on) => toggle(id, on === true)} />
                    <span className="min-w-0 truncate">{name}</span>
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
            The run starts on {fallback.order ? 'the first ticked account, top to bottom, with usage left' : 'the account here with the most usage left'} if {owner}&apos;s usage is at 95% or more at launch, and continues there if the vendor reports the limit mid-run.
          </p>
        </>
      )}
    </div>
  )
}

export function AccountOptions({
  label,
  accounts,
  ownAccountID,
  account,
  onChange,
  fallback,
  onFallback,
  swarm = false,
}: {
  label: string
  accounts: Member[]
  ownAccountID: string
  account: string
  onChange: (account: string) => void
  fallback: Fallback
  onFallback: (fallback: Fallback) => void
  swarm?: boolean
}) {
  // A swarm's list is every worker's policy too; the server skips each run's current account.
  const skip = swarm ? '' : account
  const accountName = (member: Member) => `${member.display_name}${member.id === ownAccountID ? ' (you)' : ' (shared)'}`
  const chosen = accounts.find((member) => member.id === account)
  const shared = account !== '' && account !== ownAccountID
  return (
    <Collapsible defaultOpen={shared}>
      <CollapsibleTrigger>
        <span className="font-medium">Options</span>
        {chosen && <span className="min-w-0 truncate text-muted">· {label}: {accountName(chosen)}{fallbackParams(fallback, accounts, skip).no_fallback && ' · Fallback off'}</span>}
      </CollapsibleTrigger>
      <CollapsibleContent className="flex flex-col gap-1 pt-2 pl-5">
        <Label htmlFor="launch-account">{label}</Label>
        <Select value={account} onValueChange={onChange}>
          <SelectTrigger id="launch-account">
            <SelectValue placeholder="Choose an account" />
          </SelectTrigger>
          <SelectContent>
            {accounts.map((member) => (
              <SelectItem key={member.id} value={member.id}>
                {accountName(member)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-ui-sm text-muted">
          {shared
            ? `Uses ${chosen?.display_name ?? account}'s agent login and vendor quota in your own environment, with your GitHub login. You remain its owner and actor.`
            : 'A teammate who shares their agent account appears here.'}
        </p>
        <FallbackList accounts={accounts} account={account} skip={skip} accountName={accountName} fallback={fallback} onChange={onFallback} />
      </CollapsibleContent>
    </Collapsible>
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
